/*******************************************************************************
*
* Copyright 2017 SAP SE
*
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You should have received a copy of the License along with this
* program. If not, you may obtain a copy of the License at
*
*     http://www.apache.org/licenses/LICENSE-2.0
*
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
*
*******************************************************************************/

package collector

import (
	"sort"
	"testing"
	"time"

	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/go-bits/easypg"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/test"
	"github.com/sapcc/limes/internal/test/plugins"
)

const (
	testKeystoneConfigYAML = `
		availability_zones: [ az-one, az-two ]
		discovery:
			method: --test-static
		services:
			- service_type: shared
				type: liquid
				params:
					area: shared
					test_mode: true
			- service_type: unshared
				type: liquid
				params:
					area: unshared
					test_mode: true
	`
)

func keystoneTestCluster(t *testing.T) (test.Setup, *core.Cluster) {
	s := test.NewSetup(t,
		test.WithConfig(testKeystoneConfigYAML),
	)
	return s, s.Cluster
}

func Test_ScanDomains(t *testing.T) {
	s, cluster := keystoneTestCluster(t)
	c := getCollector(t, s)
	discovery := cluster.DiscoveryPlugin.(*plugins.StaticDiscoveryPlugin)

	// construct expectation for return value
	var expectedNewDomains []string
	for _, domain := range discovery.Domains {
		expectedNewDomains = append(expectedNewDomains, domain.UUID)
	}

	// first ScanDomains should discover the StaticDomains in the cluster,
	// and initialize domains, projects and project_services (project_resources
	// are then constructed by the scraper)
	actualNewDomains, err := c.ScanDomains(s.Ctx, ScanDomainsOpts{})
	if err != nil {
		t.Errorf("ScanDomains #1 failed: %v", err)
	}
	sort.Strings(expectedNewDomains) // order does not matter
	sort.Strings(actualNewDomains)
	assert.DeepEqual(t, "new domains after ScanDomains #1", actualNewDomains, expectedNewDomains)
	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualToFile("fixtures/scandomains1.sql")

	// second ScanDomains should not discover anything new
	s.Clock.StepBy(10 * time.Minute)
	actualNewDomains, err = c.ScanDomains(s.Ctx, ScanDomainsOpts{})
	if err != nil {
		t.Errorf("ScanDomains #2 failed: %v", err)
	}
	assert.DeepEqual(t, "new domains after ScanDomains #2", actualNewDomains, []string(nil))
	tr.DBChanges().AssertEmpty()

	// add another project
	domainUUID := "uuid-for-france"
	discovery.Projects[domainUUID] = append(discovery.Projects[domainUUID],
		core.KeystoneProject{Name: "bordeaux", UUID: "uuid-for-bordeaux", ParentUUID: "uuid-for-france"},
	)

	// ScanDomains without ScanAllProjects should not see this new project
	s.Clock.StepBy(10 * time.Minute)
	actualNewDomains, err = c.ScanDomains(s.Ctx, ScanDomainsOpts{})
	if err != nil {
		t.Errorf("ScanDomains #3 failed: %v", err)
	}
	assert.DeepEqual(t, "new domains after ScanDomains #3", actualNewDomains, []string(nil))
	tr.DBChanges().AssertEmpty()

	// ScanDomains with ScanAllProjects should discover the new project
	s.Clock.StepBy(10 * time.Minute)
	actualNewDomains, err = c.ScanDomains(s.Ctx, ScanDomainsOpts{ScanAllProjects: true})
	if err != nil {
		t.Errorf("ScanDomains #4 failed: %v", err)
	}
	assert.DeepEqual(t, "new domains after ScanDomains #4", actualNewDomains, []string(nil))
	//nolint:dupword // false positive on "TRUE, TRUE"
	tr.DBChanges().AssertEqualf(`
		INSERT INTO project_services (id, project_id, type, stale, rates_stale, next_scrape_at, rates_next_scrape_at) VALUES (7, 4, 'shared', TRUE, TRUE, %[1]d, %[1]d);
		INSERT INTO project_services (id, project_id, type, stale, rates_stale, next_scrape_at, rates_next_scrape_at) VALUES (8, 4, 'unshared', TRUE, TRUE, %[1]d, %[1]d);
		INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (4, 2, 'bordeaux', 'uuid-for-bordeaux', 'uuid-for-france');
	`,
		s.Clock.Now().Unix(),
	)

	// remove the project again
	discovery.Projects[domainUUID] = discovery.Projects[domainUUID][0:1]

	// ScanDomains without ScanAllProjects should not notice anything
	s.Clock.StepBy(10 * time.Minute)
	actualNewDomains, err = c.ScanDomains(s.Ctx, ScanDomainsOpts{})
	if err != nil {
		t.Errorf("ScanDomains #5 failed: %v", err)
	}
	assert.DeepEqual(t, "new domains after ScanDomains #5", actualNewDomains, []string(nil))
	tr.DBChanges().AssertEmpty()

	// ScanDomains with ScanAllProjects should notice the deleted project and cleanup its records
	s.Clock.StepBy(10 * time.Minute)
	actualNewDomains, err = c.ScanDomains(s.Ctx, ScanDomainsOpts{ScanAllProjects: true})
	if err != nil {
		t.Errorf("ScanDomains #6 failed: %v", err)
	}
	assert.DeepEqual(t, "new domains after ScanDomains #6", actualNewDomains, []string(nil))
	tr.DBChanges().AssertEqualf(`
		DELETE FROM project_services WHERE id = 7 AND project_id = 4 AND type = 'shared';
		DELETE FROM project_services WHERE id = 8 AND project_id = 4 AND type = 'unshared';
		DELETE FROM projects WHERE id = 4 AND uuid = 'uuid-for-bordeaux';
	`)

	// remove a whole domain
	discovery.Domains = discovery.Domains[0:1]

	// ScanDomains should notice the deleted domain and cleanup its records and also its projects
	s.Clock.StepBy(10 * time.Minute)
	actualNewDomains, err = c.ScanDomains(s.Ctx, ScanDomainsOpts{})
	if err != nil {
		t.Errorf("ScanDomains #7 failed: %v", err)
	}
	assert.DeepEqual(t, "new domains after ScanDomains #7", actualNewDomains, []string(nil))
	tr.DBChanges().AssertEqualf(`
		DELETE FROM domains WHERE id = 2 AND uuid = 'uuid-for-france';
		DELETE FROM project_services WHERE id = 5 AND project_id = 3 AND type = 'shared';
		DELETE FROM project_services WHERE id = 6 AND project_id = 3 AND type = 'unshared';
		DELETE FROM projects WHERE id = 3 AND uuid = 'uuid-for-paris';
	`)

	// rename a domain and a project
	discovery.Domains[0].Name = "germany-changed"
	discovery.Projects["uuid-for-germany"][0].Name = "berlin-changed"

	// ScanDomains should notice the changed names and update the domain/project records accordingly
	s.Clock.StepBy(10 * time.Minute)
	actualNewDomains, err = c.ScanDomains(s.Ctx, ScanDomainsOpts{ScanAllProjects: true})
	if err != nil {
		t.Errorf("ScanDomains #8 failed: %v", err)
	}
	assert.DeepEqual(t, "new domains after ScanDomains #8", actualNewDomains, []string(nil))
	tr.DBChanges().AssertEqualf(`
		UPDATE domains SET name = 'germany-changed' WHERE id = 1 AND uuid = 'uuid-for-germany';
		UPDATE projects SET name = 'berlin-changed' WHERE id = 1 AND uuid = 'uuid-for-berlin';
	`)
}
