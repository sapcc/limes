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
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/go-bits/easypg"
	"gopkg.in/gorp.v2"

	"github.com/sapcc/limes/pkg/core"
	"github.com/sapcc/limes/pkg/test"
)

func keystoneTestCluster(t *testing.T) (*core.Cluster, *gorp.DbMap) {
	dbm := test.InitDatabase(t, nil)

	return &core.Cluster{
		ID:              "west",
		Config:          core.ClusterConfiguration{},
		ServiceTypes:    []string{"unshared", "shared"},
		DiscoveryPlugin: test.NewDiscoveryPlugin(),
		QuotaPlugins: map[string]core.QuotaPlugin{
			"shared":   test.NewPlugin("shared"),
			"unshared": test.NewPlugin("unshared"),
		},
		CapacityPlugins: map[string]core.CapacityPlugin{},
	}, dbm
}

func Test_ScanDomains(t *testing.T) {
	cluster, dbm := keystoneTestCluster(t)
	discovery := cluster.DiscoveryPlugin.(*test.DiscoveryPlugin) //nolint:errcheck

	//construct expectation for return value
	var expectedNewDomains []string
	for _, domain := range discovery.StaticDomains {
		expectedNewDomains = append(expectedNewDomains, domain.UUID)
	}

	//add a quota constraint set; we're going to test if it's applied correctly
	pointerTo := func(x uint64) *uint64 { return &x }
	cluster.QuotaConstraints = &core.QuotaConstraintSet{
		Domains: map[string]core.QuotaConstraints{
			"germany": {
				"unshared": {
					"things":   {Minimum: pointerTo(10)},
					"capacity": {Minimum: pointerTo(20)},
				},
			},
		},
		Projects: map[string]map[string]core.QuotaConstraints{
			"germany": {
				"berlin": {
					"unshared": {
						"things": {Minimum: pointerTo(5)},
					},
					"shared": {
						"capacity": {Minimum: pointerTo(10)},
					},
				},
			},
		},
	}

	c := Collector{
		Cluster:  cluster,
		DB:       dbm,
		LogError: t.Errorf,
		TimeNow:  test.TimeNow,
		Once:     true,
	}

	//first ScanDomains should discover the StaticDomains in the cluster,
	//and initialize domains, projects and project_services (project_resources
	//are then constructed by the scraper, and domain_services/domain_resources
	//are created when a cloud admin approves quota for the domain)
	//
	//This also tests that the quota constraint is applied correctly.
	actualNewDomains, err := c.ScanDomains(ScanDomainsOpts{})
	if err != nil {
		t.Errorf("ScanDomains #1 failed: %v", err)
	}
	sort.Strings(expectedNewDomains) //order does not matter
	sort.Strings(actualNewDomains)
	assert.DeepEqual(t, "new domains after ScanDomains #1", actualNewDomains, expectedNewDomains)
	tr, tr0 := easypg.NewTracker(t, dbm.Db)
	tr0.AssertEqualToFile("fixtures/scandomains1.sql")

	//first ScanDomains should not discover anything new
	actualNewDomains, err = c.ScanDomains(ScanDomainsOpts{})
	if err != nil {
		t.Errorf("ScanDomains #2 failed: %v", err)
	}
	assert.DeepEqual(t, "new domains after ScanDomains #2", actualNewDomains, []string(nil))
	tr.DBChanges().AssertEmpty()

	//add another project
	domainUUID := "uuid-for-france"
	discovery.StaticProjects[domainUUID] = append(discovery.StaticProjects[domainUUID],
		core.KeystoneProject{Name: "bordeaux", UUID: "uuid-for-bordeaux", ParentUUID: "uuid-for-france"},
	)

	//ScanDomains without ScanAllProjects should not see this new project
	actualNewDomains, err = c.ScanDomains(ScanDomainsOpts{})
	if err != nil {
		t.Errorf("ScanDomains #3 failed: %v", err)
	}
	assert.DeepEqual(t, "new domains after ScanDomains #3", actualNewDomains, []string(nil))
	tr.DBChanges().AssertEmpty()

	//ScanDomains with ScanAllProjects should discover the new project
	actualNewDomains, err = c.ScanDomains(ScanDomainsOpts{ScanAllProjects: true})
	if err != nil {
		t.Errorf("ScanDomains #4 failed: %v", err)
	}
	assert.DeepEqual(t, "new domains after ScanDomains #4", actualNewDomains, []string(nil))
	tr.DBChanges().AssertEqualf(`
		INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state, serialized_metrics, checked_at, scrape_error_message, rates_checked_at, rates_scrape_error_message) VALUES (7, 4, 'unshared', NULL, FALSE, 0, NULL, FALSE, 0, '', '', NULL, '', NULL, '');
		INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state, serialized_metrics, checked_at, scrape_error_message, rates_checked_at, rates_scrape_error_message) VALUES (8, 4, 'shared', NULL, FALSE, 0, NULL, FALSE, 0, '', '', NULL, '', NULL, '');
		INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (4, 2, 'bordeaux', 'uuid-for-bordeaux', 'uuid-for-france', FALSE);
	`)

	//remove the project again
	discovery.StaticProjects[domainUUID] = discovery.StaticProjects[domainUUID][0:1]

	//ScanDomains without ScanAllProjects should not notice anything
	actualNewDomains, err = c.ScanDomains(ScanDomainsOpts{})
	if err != nil {
		t.Errorf("ScanDomains #5 failed: %v", err)
	}
	assert.DeepEqual(t, "new domains after ScanDomains #5", actualNewDomains, []string(nil))
	tr.DBChanges().AssertEmpty()

	//ScanDomains with ScanAllProjects should notice the deleted project and cleanup its records
	actualNewDomains, err = c.ScanDomains(ScanDomainsOpts{ScanAllProjects: true})
	if err != nil {
		t.Errorf("ScanDomains #6 failed: %v", err)
	}
	assert.DeepEqual(t, "new domains after ScanDomains #6", actualNewDomains, []string(nil))
	tr.DBChanges().AssertEqualf(`
		DELETE FROM project_services WHERE id = 7 AND project_id = 4 AND type = 'unshared';
		DELETE FROM project_services WHERE id = 8 AND project_id = 4 AND type = 'shared';
		DELETE FROM projects WHERE id = 4 AND uuid = 'uuid-for-bordeaux';
	`)

	//remove a whole domain
	discovery.StaticDomains = discovery.StaticDomains[0:1]

	//ScanDomains should notice the deleted domain and cleanup its records and also its projects
	actualNewDomains, err = c.ScanDomains(ScanDomainsOpts{})
	if err != nil {
		t.Errorf("ScanDomains #7 failed: %v", err)
	}
	assert.DeepEqual(t, "new domains after ScanDomains #7", actualNewDomains, []string(nil))
	tr.DBChanges().AssertEqualf(`
		DELETE FROM domain_services WHERE id = 3 AND domain_id = 2 AND type = 'unshared';
		DELETE FROM domain_services WHERE id = 4 AND domain_id = 2 AND type = 'shared';
		DELETE FROM domains WHERE id = 2 AND cluster_id = 'west' AND uuid = 'uuid-for-france';
		DELETE FROM project_services WHERE id = 5 AND project_id = 3 AND type = 'unshared';
		DELETE FROM project_services WHERE id = 6 AND project_id = 3 AND type = 'shared';
		DELETE FROM projects WHERE id = 3 AND uuid = 'uuid-for-paris';
	`)

	//rename a domain and a project
	discovery.StaticDomains[0].Name = "germany-changed"
	discovery.StaticProjects["uuid-for-germany"][0].Name = "berlin-changed"

	//ScanDomains should notice the changed names and update the domain/project records accordingly
	actualNewDomains, err = c.ScanDomains(ScanDomainsOpts{ScanAllProjects: true})
	if err != nil {
		t.Errorf("ScanDomains #8 failed: %v", err)
	}
	assert.DeepEqual(t, "new domains after ScanDomains #8", actualNewDomains, []string(nil))
	tr.DBChanges().AssertEqualf(`
		UPDATE domains SET name = 'germany-changed' WHERE id = 1 AND cluster_id = 'west' AND uuid = 'uuid-for-germany';
		UPDATE projects SET name = 'berlin-changed' WHERE id = 1 AND uuid = 'uuid-for-berlin';
	`)
}

func Test_listDomainsFiltered(t *testing.T) {
	cluster := &core.Cluster{
		DiscoveryPlugin: &test.DiscoveryPlugin{
			StaticDomains: []core.KeystoneDomain{
				{Name: "bar1"},
				{Name: "bar2"},
				{Name: "foo1"},
				{Name: "foo2"},
			},
		},
		Config: core.ClusterConfiguration{
			Discovery: core.DiscoveryConfiguration{
				IncludeDomainRx: regexp.MustCompile(`foo`),
				ExcludeDomainRx: regexp.MustCompile(`2$`),
			},
		},
	}

	domains, err := (&Collector{Cluster: cluster}).listDomainsFiltered()
	if err != nil {
		t.Fatal("listDomainsFiltered failed with unexpected error:", err.Error())
	}
	names := make([]string, len(domains))
	for idx, d := range domains {
		names[idx] = d.Name
	}
	namesStr := strings.Join(names, " ")
	if namesStr != "foo1" {
		t.Errorf("expected only domain \"foo1\", but got \"%s\" instead", namesStr)
	}
}
