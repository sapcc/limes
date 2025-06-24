// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"encoding/json"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/majewsky/gg/option"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/go-bits/easypg"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/test"
)

const (
	testKeystoneConfigYAML = `
		availability_zones: [ az-one, az-two ]
		discovery:
			method: static
			static_config:
				domains:
					- { name: germany, id: uuid-for-germany }
					- { name: france,id: uuid-for-france }
				projects:
					uuid-for-germany:
						- { name: berlin, id: uuid-for-berlin, parent_id: uuid-for-germany }
						- { name: dresden, id: uuid-for-dresden, parent_id: uuid-for-berlin }
					uuid-for-france:
						- { name: paris, id: uuid-for-paris, parent_id: uuid-for-france}
		liquids:
			shared:
				area: shared
				liquid_service_type: %[1]s
			unshared:
				area: unshared
				liquid_service_type: %[1]s
	`
)

func keystoneTestCluster(t *testing.T) (test.Setup, *core.Cluster) {
	srvInfo := test.DefaultLiquidServiceInfo()
	_, liquidServiceType := test.NewMockLiquidClient(srvInfo)
	s := test.NewSetup(t,
		test.WithConfig(fmt.Sprintf(testKeystoneConfigYAML, liquidServiceType)),
		// the functions called from the tests of this setup run in collect task, so we use the LiquidConnections
		test.WithLiquidConnections,
	)
	return s, s.Cluster
}

func Test_ScanDomains(t *testing.T) {
	s, cluster := keystoneTestCluster(t)
	c := getCollector(t, s)
	discovery := cluster.DiscoveryPlugin.(*core.StaticDiscoveryPlugin)

	// construct expectation for return value
	var expectedNewDomains []string
	for _, domain := range discovery.Config.Domains {
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
	discovery.Config.Projects[domainUUID] = append(discovery.Config.Projects[domainUUID],
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
	tr.DBChanges().AssertEqualf(`
		INSERT INTO project_services_v2 (id, project_id, service_id, stale, next_scrape_at) VALUES (7, 4, 1, TRUE, %[1]d);
		INSERT INTO project_services_v2 (id, project_id, service_id, stale, next_scrape_at) VALUES (8, 4, 2, TRUE, %[1]d);
		INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (4, 2, 'bordeaux', 'uuid-for-bordeaux', 'uuid-for-france');
	`,
		s.Clock.Now().Unix(),
	)

	// remove the project again
	discovery.Config.Projects[domainUUID] = discovery.Config.Projects[domainUUID][0:1]

	// ScanDomains without ScanAllProjects should not notice anything
	s.Clock.StepBy(10 * time.Minute)
	actualNewDomains, err = c.ScanDomains(s.Ctx, ScanDomainsOpts{})
	if err != nil {
		t.Errorf("ScanDomains #5 failed: %v", err)
	}
	assert.DeepEqual(t, "new domains after ScanDomains #5", actualNewDomains, []string(nil))
	tr.DBChanges().AssertEmpty()

	// ScanDomains with ScanAllProjects should notice the deleted project and don't cleanup because of active commitments
	commitmentForOneDay, err := limesresources.ParseCommitmentDuration("1 day")
	mustT(t, err)
	creationContext := db.CommitmentWorkflowContext{Reason: db.CommitmentReasonCreate}
	buf, err := json.Marshal(creationContext)
	mustT(t, err)
	mustT(t, s.DB.Insert(&db.ProjectCommitmentV2{
		UUID:                "00000000-0000-0000-0000-000000000001",
		ProjectID:           4,
		AZResourceID:        1,
		Amount:              10,
		Duration:            commitmentForOneDay,
		CreatedAt:           s.Clock.Now(),
		CreatorUUID:         "dummy",
		CreatorName:         "dummy",
		ConfirmedAt:         option.Some(s.Clock.Now()),
		ExpiresAt:           commitmentForOneDay.AddTo(s.Clock.Now()),
		State:               db.CommitmentStateActive,
		CreationContextJSON: buf,
	}))
	tr.DBChanges().Ignore()
	s.Clock.StepBy(10 * time.Minute)
	_, err = c.ScanDomains(s.Ctx, ScanDomainsOpts{ScanAllProjects: true})
	if err == nil {
		t.Errorf("ScanDomains #6 did not fail when it should have")
	}
	assert.DeepEqual(t, "error string after ScanDomains #6", err.Error(), "while removing deleted Keystone project france/bordeaux from our database: project has commitments which are not superseeded or expired")
	tr.DBChanges().AssertEmpty()

	// now we set the commitment to expired, the deletion succeeds
	_, err = s.DB.Exec(`UPDATE project_commitments_v2 SET state = $1`, db.CommitmentStateExpired)
	mustT(t, err)
	s.Clock.StepBy(10 * time.Minute)
	actualNewDomains, err = c.ScanDomains(s.Ctx, ScanDomainsOpts{ScanAllProjects: true})
	if err != nil {
		t.Errorf("ScanDomains #7 failed: %v", err)
	}
	assert.DeepEqual(t, "new domains after ScanDomains #6", actualNewDomains, []string(nil))
	tr.DBChanges().AssertEqualf(`
		DELETE FROM project_commitments_v2 WHERE id = 1 AND transfer_token = NULL AND uuid = '00000000-0000-0000-0000-000000000001';
		DELETE FROM project_services_v2 WHERE id = 7 AND project_id = 4 AND service_id = 1;
		DELETE FROM project_services_v2 WHERE id = 8 AND project_id = 4 AND service_id = 2;
		DELETE FROM projects WHERE id = 4 AND uuid = 'uuid-for-bordeaux';
	`)

	// remove a whole domain
	discovery.Config.Domains = discovery.Config.Domains[0:1]

	// ScanDomains should notice the deleted domain and cleanup its records and also its projects
	s.Clock.StepBy(10 * time.Minute)
	actualNewDomains, err = c.ScanDomains(s.Ctx, ScanDomainsOpts{})
	if err != nil {
		t.Errorf("ScanDomains #8 failed: %v", err)
	}
	assert.DeepEqual(t, "new domains after ScanDomains #7", actualNewDomains, []string(nil))
	tr.DBChanges().AssertEqualf(`
		DELETE FROM domains WHERE id = 2 AND uuid = 'uuid-for-france';
		DELETE FROM project_services_v2 WHERE id = 5 AND project_id = 3 AND service_id = 1;
		DELETE FROM project_services_v2 WHERE id = 6 AND project_id = 3 AND service_id = 2;
		DELETE FROM projects WHERE id = 3 AND uuid = 'uuid-for-paris';
	`)

	// rename a domain and a project
	discovery.Config.Domains[0].Name = "germany-changed"
	discovery.Config.Projects["uuid-for-germany"][0].Name = "berlin-changed"

	// ScanDomains should notice the changed names and update the domain/project records accordingly
	s.Clock.StepBy(10 * time.Minute)
	actualNewDomains, err = c.ScanDomains(s.Ctx, ScanDomainsOpts{ScanAllProjects: true})
	if err != nil {
		t.Errorf("ScanDomains #9 failed: %v", err)
	}
	assert.DeepEqual(t, "new domains after ScanDomains #8", actualNewDomains, []string(nil))
	tr.DBChanges().AssertEqualf(`
		UPDATE domains SET name = 'germany-changed' WHERE id = 1 AND uuid = 'uuid-for-germany';
		UPDATE projects SET name = 'berlin-changed' WHERE id = 1 AND uuid = 'uuid-for-berlin';
	`)
}
