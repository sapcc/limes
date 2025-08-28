// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package core_test

import (
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/errext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/test"
)

const (
	testConfigJSON = `{
		"availability_zones": ["az-one", "az-two"],
		"discovery": {
			"method": "static",
			"static_config": {
				"domains": [
					{"name": "germany", "id": "uuid-for-germany"}
				],
				"projects": {
					"uuid-for-germany": [
						{"name": "berlin", "id": "uuid-for-berlin", "parent_id": "uuid-for-germany"}
					]
				}
			}
		},
		"liquids": {
			"shared": {
				"area": "shared"
			},
			"unshared": {
				"area": "unshared",
				"rate_limits": {
					"global": [
						{"name": "in_global_and_liquid", "limit": 10, "window": "1m"},
						{"name": "in_global_and_project", "limit": 20, "window": "1m"},
						{"name": "only_in_global", "limit": 30, "window": "1m"}
					],
					"project_default": [
						{"name": "in_global_and_project", "limit": 2, "window": "1m"},
						{"name": "in_liquid_and_project", "limit": 4, "window": "1m"},
						{"name": "only_in_project", "limit": 5, "window": "1m"}
					]
				}
			}
		}
	}`
)

func generateNewClusterWithPersistingServiceInfo(t *testing.T, s test.Setup) {
	liquidClientFactory := s.Cluster.LiquidClientFactory
	var errs errext.ErrorSet
	s.Cluster, errs = core.NewClusterFromJSON([]byte(testConfigJSON), s.Clock.Now, s.DB, true)
	for _, err := range errs {
		t.Fatal(err)
	}
	errs = s.Cluster.Connect(s.Ctx, nil, gophercloud.EndpointOpts{}, liquidClientFactory)
	for _, err := range errs {
		t.Fatal(err)
	}
}

func TestMain(m *testing.M) {
	easypg.WithTestDB(m, func() int { return m.Run() })
}

func Test_ClusterSaveServiceInfo(t *testing.T) {
	srvInfoShared := test.DefaultLiquidServiceInfo()
	srvInfoUnshared := test.DefaultLiquidServiceInfo()
	srvInfoUnshared.Rates = map[liquid.RateName]liquid.RateInfo{
		"in_global_and_liquid":  {Unit: liquid.UnitMebibytes, Topology: liquid.FlatTopology, HasUsage: true},
		"in_liquid_and_project": {Unit: liquid.UnitMebibytes, Topology: liquid.FlatTopology, HasUsage: true},
		"only_in_liquid":        {Unit: liquid.UnitMebibytes, Topology: liquid.FlatTopology, HasUsage: true},
	}

	s := test.NewSetup(t,
		test.WithConfig(testConfigJSON),
		test.WithPersistedServiceInfo("shared", srvInfoShared),
		test.WithMockLiquidClient("shared", srvInfoShared),
		test.WithMockLiquidClient("unshared", srvInfoUnshared),
	)

	// We now have a situation where one service is persisted into the database.
	// First, check that on a new cluster with LiquidConnections (collect task) the second service is saved correctly.
	tr, _ := easypg.NewTracker(t, s.DB.Db)
	generateNewClusterWithPersistingServiceInfo(t, s)
	tr.DBChanges().AssertEqualf(`
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (10, 3, 'az-two', 0, 'unshared/capacity/az-two');
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (11, 3, 'total', 0, 'unshared/capacity/total');
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (12, 3, 'unknown', 0, 'unshared/capacity/unknown');
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (13, 4, 'any', 0, 'unshared/things/any');
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (14, 4, 'total', 0, 'unshared/things/total');
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (8, 3, 'any', 0, 'unshared/capacity/any');
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (9, 3, 'az-one', 0, 'unshared/capacity/az-one');
		INSERT INTO rates (id, service_id, name, liquid_version, unit, topology, has_usage) VALUES (1, 2, 'in_global_and_liquid', 1, 'MiB', 'flat', TRUE);
		INSERT INTO rates (id, service_id, name, liquid_version, topology) VALUES (2, 2, 'in_global_and_project', 1, 'flat');
		INSERT INTO rates (id, service_id, name, liquid_version, unit, topology, has_usage) VALUES (3, 2, 'in_liquid_and_project', 1, 'MiB', 'flat', TRUE);
		INSERT INTO rates (id, service_id, name, liquid_version, topology) VALUES (4, 2, 'only_in_global', 1, 'flat');
		INSERT INTO rates (id, service_id, name, liquid_version, unit, topology, has_usage) VALUES (5, 2, 'only_in_liquid', 1, 'MiB', 'flat', TRUE);
		INSERT INTO rates (id, service_id, name, liquid_version, topology) VALUES (6, 2, 'only_in_project', 1, 'flat');
		INSERT INTO resources (id, service_id, name, liquid_version, unit, topology, has_capacity, needs_resource_demand, has_quota, path) VALUES (3, 2, 'capacity', 1, 'B', 'az-aware', TRUE, TRUE, TRUE, 'unshared/capacity');
		INSERT INTO resources (id, service_id, name, liquid_version, topology, has_quota, path) VALUES (4, 2, 'things', 1, 'flat', TRUE, 'unshared/things');
		INSERT INTO services (id, type, next_scrape_at, liquid_version) VALUES (2, 'unshared', 0, 1);
	`)

	// Now, we update the serviceInfo of the shared service, updates should be done
	s.LiquidClients["shared"].ServiceInfo.Modify(func(info *liquid.ServiceInfo) {
		info.Version = 2
		delete(info.Resources, "things") // remove things resource
	})
	generateNewClusterWithPersistingServiceInfo(t, s)
	tr.DBChanges().AssertEqual(`
		DELETE FROM az_resources WHERE id = 6 AND resource_id = 2 AND az = 'any' AND path = 'shared/things/any';
		DELETE FROM az_resources WHERE id = 7 AND resource_id = 2 AND az = 'total' AND path = 'shared/things/total';
		UPDATE resources SET liquid_version = 2 WHERE id = 1 AND service_id = 1 AND name = 'capacity' AND path = 'shared/capacity';
		DELETE FROM resources WHERE id = 2 AND service_id = 1 AND name = 'things' AND path = 'shared/things';
		DELETE FROM services WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
		INSERT INTO services (id, type, next_scrape_at, liquid_version) VALUES (1, 'shared', 0, 2);
	`)

	// Now, we add the "things" resource back to the shared service, it should be added again.
	s.LiquidClients["shared"].ServiceInfo.Modify(func(info *liquid.ServiceInfo) {
		*info = test.DefaultLiquidServiceInfo()
		info.Version = 3
	})
	generateNewClusterWithPersistingServiceInfo(t, s)
	tr.DBChanges().AssertEqual(`
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (15, 5, 'any', 0, 'shared/things/any');
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (16, 5, 'total', 0, 'shared/things/total');
		UPDATE resources SET liquid_version = 3 WHERE id = 1 AND service_id = 1 AND name = 'capacity' AND path = 'shared/capacity';
		INSERT INTO resources (id, service_id, name, liquid_version, topology, has_quota, path) VALUES (5, 1, 'things', 3, 'flat', TRUE, 'shared/things');
		DELETE FROM services WHERE id = 1 AND type = 'shared' AND liquid_version = 2;
		INSERT INTO services (id, type, next_scrape_at, liquid_version) VALUES (1, 'shared', 0, 3);
	`)

	// just an increase of the LiquidVersion
	s.LiquidClients["shared"].ServiceInfo.Modify(func(info *liquid.ServiceInfo) {
		info.Version = 4
	})
	generateNewClusterWithPersistingServiceInfo(t, s)
	tr.DBChanges().AssertEqual(`
		UPDATE resources SET liquid_version = 4 WHERE id = 1 AND service_id = 1 AND name = 'capacity' AND path = 'shared/capacity';
		UPDATE resources SET liquid_version = 4 WHERE id = 5 AND service_id = 1 AND name = 'things' AND path = 'shared/things';
		DELETE FROM services WHERE id = 1 AND type = 'shared' AND liquid_version = 3;
		INSERT INTO services (id, type, next_scrape_at, liquid_version) VALUES (1, 'shared', 0, 4);
	`)
}
