// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package core_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/errext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/test"
)

const (
	testConfigYAML = `
		availability_zones: [ az-one, az-two ]
		discovery:
			method: static
			static_config:
				domains:
					- { name: germany, id: uuid-for-germany }
				projects:
					uuid-for-germany:
						- { name: berlin, id: uuid-for-berlin, parent_id: uuid-for-germany }
		liquids:
			shared:
				area: shared
				liquid_service_type: %[1]s
			unshared:
				area: unshared
				liquid_service_type: %[2]s
	`
)

func generateNewClusterWithPersistingServiceInfo(t *testing.T, s test.Setup, replacedConfig string) {
	var errs errext.ErrorSet
	s.Cluster, errs = core.NewClusterFromYAML([]byte(strings.ReplaceAll(replacedConfig, "\t", "  ")), nil, gophercloud.EndpointOpts{}, s.Clock.Now, s.DB, true)
	for _, err := range errs {
		t.Fatal(err)
	}
	errs = s.Cluster.Connect(s.Ctx)
	for _, err := range errs {
		t.Fatal(err)
	}
}

func TestMain(m *testing.M) {
	easypg.WithTestDB(m, func() int { return m.Run() })
}

// We have to put this into the test package, because we are testing something which is baked into the Setup in a normal test.
func Test_ClusterSaveServiceInfo(t *testing.T) {
	srvInfoShared := test.DefaultLiquidServiceInfo()
	srvInfoUnshared := test.DefaultLiquidServiceInfo()
	liquidClientShared, liquidServiceTypeShared := test.NewMockLiquidClient(srvInfoShared)
	_, liquidServiceTypeUnshared := test.NewMockLiquidClient(srvInfoUnshared)

	replacedConfig := fmt.Sprintf(testConfigYAML, liquidServiceTypeShared, liquidServiceTypeUnshared)

	s := test.NewSetup(t,
		test.WithConfig(replacedConfig),
		test.WithPersistedServiceInfo("shared", srvInfoShared),
	)

	// We now have a situation where one service is persisted into the database.
	// First, check that on a new cluster with LiquidConnections (collect task) the second service is saved correctly.
	tr, _ := easypg.NewTracker(t, s.DB.Db)
	generateNewClusterWithPersistingServiceInfo(t, s, replacedConfig)
	tr.DBChanges().AssertEqualf(`
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (10, 4, 'any', 0, 'unshared/things/any');
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (6, 3, 'any', 0, 'unshared/capacity/any');
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (7, 3, 'az-one', 0, 'unshared/capacity/az-one');
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (8, 3, 'az-two', 0, 'unshared/capacity/az-two');
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (9, 3, 'unknown', 0, 'unshared/capacity/unknown');
		INSERT INTO resources (id, service_id, name, liquid_version, unit, topology, has_capacity, needs_resource_demand, has_quota, path) VALUES (3, 2, 'capacity', 1, 'B', 'az-aware', TRUE, TRUE, TRUE, 'unshared/capacity');
		INSERT INTO resources (id, service_id, name, liquid_version, topology, has_quota, path) VALUES (4, 2, 'things', 1, 'flat', TRUE, 'unshared/things');
		INSERT INTO services (id, type, next_scrape_at, liquid_version) VALUES (2, 'unshared', 0, 1);
	`)

	// Now, we update the serviceInfo of the shared service, updates should be done
	srvInfoShared.Version = 2
	delete(srvInfoShared.Resources, "things") // remove things resource
	liquidClientShared.SetServiceInfo(srvInfoShared)
	generateNewClusterWithPersistingServiceInfo(t, s, replacedConfig)
	tr.DBChanges().AssertEqual(`
		DELETE FROM az_resources WHERE id = 5 AND resource_id = 2 AND az = 'any' AND path = 'shared/things/any';
		UPDATE resources SET liquid_version = 2 WHERE id = 1 AND service_id = 1 AND name = 'capacity' AND path = 'shared/capacity';
		DELETE FROM resources WHERE id = 2 AND service_id = 1 AND name = 'things' AND path = 'shared/things';
		DELETE FROM services WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
		INSERT INTO services (id, type, next_scrape_at, liquid_version) VALUES (1, 'shared', 0, 2);
	`)

	// Now, we add the "things" resource back to the shared service, it should be added again.
	srvInfoShared = test.DefaultLiquidServiceInfo()
	srvInfoShared.Version = 3
	liquidClientShared.SetServiceInfo(srvInfoShared)
	generateNewClusterWithPersistingServiceInfo(t, s, replacedConfig)
	tr.DBChanges().AssertEqual(`
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (11, 5, 'any', 0, 'shared/things/any');
		UPDATE resources SET liquid_version = 3 WHERE id = 1 AND service_id = 1 AND name = 'capacity' AND path = 'shared/capacity';
		INSERT INTO resources (id, service_id, name, liquid_version, topology, has_quota, path) VALUES (5, 1, 'things', 3, 'flat', TRUE, 'shared/things');
		DELETE FROM services WHERE id = 1 AND type = 'shared' AND liquid_version = 2;
		INSERT INTO services (id, type, next_scrape_at, liquid_version) VALUES (1, 'shared', 0, 3);
	`)

	// just an increase of the LiquidVersion
	srvInfoShared.Version = 4
	liquidClientShared.SetServiceInfo(srvInfoShared)
	generateNewClusterWithPersistingServiceInfo(t, s, replacedConfig)
	tr.DBChanges().AssertEqual(`
		UPDATE resources SET liquid_version = 4 WHERE id = 1 AND service_id = 1 AND name = 'capacity' AND path = 'shared/capacity';
		UPDATE resources SET liquid_version = 4 WHERE id = 5 AND service_id = 1 AND name = 'things' AND path = 'shared/things';
		DELETE FROM services WHERE id = 1 AND type = 'shared' AND liquid_version = 3;
		INSERT INTO services (id, type, next_scrape_at, liquid_version) VALUES (1, 'shared', 0, 4);
	`)
}
