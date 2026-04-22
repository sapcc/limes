// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package core_test

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	. "github.com/majewsky/gg/option"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/errext"
	"github.com/sapcc/go-bits/must"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
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
		"areas": { "shared": { "display_name": "Shared" }, "unshared": { "display_name": "Unshared" }},
		"liquids": {
			"shared": {
				"area": "shared"
			},
			"unshared": {
				"area": "unshared",
				"rate_limits": {
					"global": [
						{"name": "in_global_and_liquid", "limit": 10, "window": "1m", "unit": "MiB"},
						{"name": "in_global_and_project", "limit": 20, "window": "1m"},
						{"name": "only_in_global", "limit": 30, "window": "1m"}
					],
					"project_default": [
						{"name": "in_global_and_project", "limit": 2, "window": "1m"},
						{"name": "in_liquid_and_project", "limit": 4, "window": "1m", "unit": "MiB"},
						{"name": "only_in_project", "limit": 5, "window": "1m"}
					]
				}
			}
		}
	}`
)

func generateNewClusterWithPersistingServiceInfo(t *testing.T, s test.Setup, failOnConnectError bool) (connectErrs errext.ErrorSet) {
	liquidClientFactory := s.Cluster.LiquidClientFactory
	var errs errext.ErrorSet
	s.Cluster, errs = core.NewClusterFromJSON([]byte(testConfigJSON), s.Clock.Now, s.DB, true)
	for _, err := range errs {
		t.Fatal(err)
	}
	connectErrs = s.Cluster.Connect(s.Ctx, nil, gophercloud.EndpointOpts{}, liquidClientFactory, None[url.URL]())
	if failOnConnectError {
		for _, err := range errs {
			t.Fatal(err)
		}
	}
	return connectErrs
}

func TestMain(m *testing.M) {
	easypg.WithTestDB(m, func() int { return m.Run() })
}

func Test_ClusterSaveServiceInfo(t *testing.T) {
	srvInfoShared := test.DefaultLiquidServiceInfo("Shared")
	srvInfoUnshared := test.DefaultLiquidServiceInfo("Unshared")
	srvInfoUnshared.Rates = map[liquid.RateName]liquid.RateInfo{
		"in_global_and_liquid":  {Unit: liquid.UnitMebibytes, Topology: liquid.FlatTopology, HasUsage: true},
		"in_liquid_and_project": {Unit: liquid.UnitMebibytes, Topology: liquid.FlatTopology, HasUsage: true},
		"only_in_liquid":        {Unit: liquid.UnitMebibytes, Topology: liquid.FlatTopology, HasUsage: true, Category: Some(liquid.CategoryName("foo_category"))},
	}

	s := test.NewSetup(t,
		test.WithConfig(testConfigJSON),
		test.WithInitialDiscovery,
		test.WithPersistedServiceInfo("shared", srvInfoShared),
		test.WithMockLiquidClient("shared", srvInfoShared),
		test.WithMockLiquidClient("unshared", srvInfoUnshared),
	)

	// We now have a situation where one service is persisted into the database.
	// First, check that on a new cluster with LiquidConnections (collect task) the second service is saved correctly.
	tr, _ := easypg.NewTracker(t, s.DB.Db)
	generateNewClusterWithPersistingServiceInfo(t, s, true)
	tr.DBChanges().AssertEqualf(`
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (10, 3, 'az-two', 0, 'unshared/capacity/az-two');
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (11, 3, 'total', 0, 'unshared/capacity/total');
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (12, 3, 'unknown', 0, 'unshared/capacity/unknown');
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (13, 4, 'any', 0, 'unshared/things/any');
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (14, 4, 'total', 0, 'unshared/things/total');
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (8, 3, 'any', 0, 'unshared/capacity/any');
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (9, 3, 'az-one', 0, 'unshared/capacity/az-one');
		INSERT INTO rates (id, service_id, name, liquid_version, unit, topology, has_usage, path) VALUES (1, 2, 'in_global_and_liquid', 1, 'MiB', 'flat', TRUE, 'unshared/in_global_and_liquid');
		INSERT INTO rates (id, service_id, name, liquid_version, topology, path) VALUES (2, 2, 'in_global_and_project', 1, 'flat', 'unshared/in_global_and_project');
		INSERT INTO rates (id, service_id, name, liquid_version, unit, topology, has_usage, path) VALUES (3, 2, 'in_liquid_and_project', 1, 'MiB', 'flat', TRUE, 'unshared/in_liquid_and_project');
		INSERT INTO rates (id, service_id, name, liquid_version, topology, path) VALUES (4, 2, 'only_in_global', 1, 'flat', 'unshared/only_in_global');
		INSERT INTO rates (id, service_id, name, liquid_version, unit, topology, has_usage, path, category_id) VALUES (5, 2, 'only_in_liquid', 1, 'MiB', 'flat', TRUE, 'unshared/only_in_liquid', 1);
		INSERT INTO rates (id, service_id, name, liquid_version, topology, path) VALUES (6, 2, 'only_in_project', 1, 'flat', 'unshared/only_in_project');
		INSERT INTO resources (id, service_id, name, liquid_version, unit, topology, has_capacity, needs_resource_demand, has_quota, path, display_name, category_id) VALUES (3, 2, 'capacity', 1, 'B', 'az-aware', TRUE, TRUE, TRUE, 'unshared/capacity', 'Capacity', 1);
		INSERT INTO resources (id, service_id, name, liquid_version, topology, has_quota, path, display_name) VALUES (4, 2, 'things', 1, 'flat', TRUE, 'unshared/things', 'Things');
		INSERT INTO services (id, type, next_scrape_at, liquid_version, display_name) VALUES (2, 'unshared', 0, 1, 'Unshared');
	`)

	// Now, we update the serviceInfo of the shared service, updates should be done
	s.LiquidClients["shared"].ServiceInfo.Modify(func(info *liquid.ServiceInfo) {
		info.Version = 2
		delete(info.Resources, "things") // remove things resource
	})
	generateNewClusterWithPersistingServiceInfo(t, s, true)
	tr.DBChanges().AssertEqual(`
		DELETE FROM az_resources WHERE id = 6 AND resource_id = 2 AND az = 'any' AND path = 'shared/things/any';
		DELETE FROM az_resources WHERE id = 7 AND resource_id = 2 AND az = 'total' AND path = 'shared/things/total';
		UPDATE resources SET liquid_version = 2 WHERE id = 1 AND service_id = 1 AND name = 'capacity' AND path = 'shared/capacity';
		DELETE FROM resources WHERE id = 2 AND service_id = 1 AND name = 'things' AND path = 'shared/things';
		DELETE FROM services WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
		INSERT INTO services (id, type, next_scrape_at, liquid_version, display_name) VALUES (1, 'shared', 0, 2, 'Shared');
	`)

	// Now, we add the "things" resource back to the shared service, it should be added again.
	s.LiquidClients["shared"].ServiceInfo.Modify(func(info *liquid.ServiceInfo) {
		*info = test.DefaultLiquidServiceInfo("Shared")
		info.Version = 3
	})
	generateNewClusterWithPersistingServiceInfo(t, s, true)
	tr.DBChanges().AssertEqual(`
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (15, 5, 'any', 0, 'shared/things/any');
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (16, 5, 'total', 0, 'shared/things/total');
		UPDATE resources SET liquid_version = 3 WHERE id = 1 AND service_id = 1 AND name = 'capacity' AND path = 'shared/capacity';
		INSERT INTO resources (id, service_id, name, liquid_version, topology, has_quota, path, display_name) VALUES (5, 1, 'things', 3, 'flat', TRUE, 'shared/things', 'Things');
		DELETE FROM services WHERE id = 1 AND type = 'shared' AND liquid_version = 2;
		INSERT INTO services (id, type, next_scrape_at, liquid_version, display_name) VALUES (1, 'shared', 0, 3, 'Shared');
	`)

	// just an increase of the LiquidVersion
	s.LiquidClients["shared"].ServiceInfo.Modify(func(info *liquid.ServiceInfo) {
		info.Version = 4
	})
	generateNewClusterWithPersistingServiceInfo(t, s, true)
	tr.DBChanges().AssertEqual(`
		UPDATE resources SET liquid_version = 4 WHERE id = 1 AND service_id = 1 AND name = 'capacity' AND path = 'shared/capacity';
		UPDATE resources SET liquid_version = 4 WHERE id = 5 AND service_id = 1 AND name = 'things' AND path = 'shared/things';
		DELETE FROM services WHERE id = 1 AND type = 'shared' AND liquid_version = 3;
		INSERT INTO services (id, type, next_scrape_at, liquid_version, display_name) VALUES (1, 'shared', 0, 4, 'Shared');
	`)

	// now we want to do an update of some categories:
	newCategories := srvInfoUnshared.Categories
	newCategories["bar_category"] = liquid.CategoryInfo{
		DisplayName: "Foo Category",
	}
	srvInfoUnshared.Categories = newCategories
	srvInfoUnshared.Rates["in_global_and_liquid"] = liquid.RateInfo{Unit: liquid.UnitMebibytes, Topology: liquid.FlatTopology, HasUsage: true, Category: Some(liquid.CategoryName("foo_category"))}
	srvInfoUnshared.Rates["only_in_liquid"] = liquid.RateInfo{Unit: liquid.UnitMebibytes, Topology: liquid.FlatTopology, HasUsage: true, Category: Some(liquid.CategoryName("bar_category"))}
	srvInfoUnshared.Version = 2
	s.LiquidClients["unshared"].ServiceInfo.Set(srvInfoUnshared)
	generateNewClusterWithPersistingServiceInfo(t, s, true)
	tr.DBChanges().AssertEqual(`
		INSERT INTO categories (id, name, display_name) VALUES (2, 'bar_category', 'Foo Category');
		UPDATE rates SET liquid_version = 2, category_id = 1 WHERE id = 1 AND service_id = 2 AND name = 'in_global_and_liquid' AND path = 'unshared/in_global_and_liquid';
		UPDATE rates SET liquid_version = 2 WHERE id = 2 AND service_id = 2 AND name = 'in_global_and_project' AND path = 'unshared/in_global_and_project';
		UPDATE rates SET liquid_version = 2 WHERE id = 3 AND service_id = 2 AND name = 'in_liquid_and_project' AND path = 'unshared/in_liquid_and_project';
		UPDATE rates SET liquid_version = 2 WHERE id = 4 AND service_id = 2 AND name = 'only_in_global' AND path = 'unshared/only_in_global';
		UPDATE rates SET liquid_version = 2, category_id = 2 WHERE id = 5 AND service_id = 2 AND name = 'only_in_liquid' AND path = 'unshared/only_in_liquid';
		UPDATE rates SET liquid_version = 2 WHERE id = 6 AND service_id = 2 AND name = 'only_in_project' AND path = 'unshared/only_in_project';
		UPDATE resources SET liquid_version = 2 WHERE id = 3 AND service_id = 2 AND name = 'capacity' AND path = 'unshared/capacity';
		UPDATE resources SET liquid_version = 2 WHERE id = 4 AND service_id = 2 AND name = 'things' AND path = 'unshared/things';
		DELETE FROM services WHERE id = 2 AND type = 'unshared' AND liquid_version = 1;
		INSERT INTO services (id, type, next_scrape_at, liquid_version, display_name) VALUES (2, 'unshared', 0, 2, 'Unshared');
	`)

	// When we remove a resource for which commitments exist, we want to fail
	// softly so that the startup is not completely interrupted. Instead, every
	// subsequent scrape will fail from there.
	sharedThingsAny := s.GetAZResourceID("shared", "things", "any")
	berlin := s.GetProjectID("berlin")
	s.MustDBInsert(&db.ProjectCommitment{
		UUID:                s.Collector.GenerateProjectCommitmentUUID(),
		ProjectID:           berlin,
		AZResourceID:        sharedThingsAny,
		Amount:              10,
		Duration:            must.Return(limesresources.ParseCommitmentDuration("10 days")),
		CreatedAt:           s.Clock.Now(),
		CreatorUUID:         "dummy",
		CreatorName:         "dummy",
		ConfirmedAt:         Some(s.Clock.Now()),
		ExpiresAt:           s.Clock.Now().AddDate(1, 0, 0),
		CreationContextJSON: must.Return(json.Marshal(db.CommitmentWorkflowContext{Reason: db.CommitmentReasonCreate})),
		Status:              liquid.CommitmentStatusConfirmed,
	})
	s.LiquidClients["shared"].ServiceInfo.Modify(func(info *liquid.ServiceInfo) {
		delete(info.Resources, "things")
		info.Version = 5
	})
	tr.DBChanges().Ignore()
	generateNewClusterWithPersistingServiceInfo(t, s, true)
	tr.DBChanges().AssertEqual("")
}

func Test_ClusterServiceInfoUnitsChange(t *testing.T) {
	srvInfoShared := test.DefaultLiquidServiceInfo("Shared")
	srvInfoUnshared := test.DefaultLiquidServiceInfo("Unshared")
	srvInfoUnshared.Rates = map[liquid.RateName]liquid.RateInfo{
		"in_global_and_liquid":  {Unit: liquid.UnitMebibytes, Topology: liquid.FlatTopology, HasUsage: true},
		"in_liquid_and_project": {Unit: liquid.UnitMebibytes, Topology: liquid.FlatTopology, HasUsage: true},
		"only_in_liquid":        {Unit: liquid.UnitMebibytes, Topology: liquid.FlatTopology, HasUsage: true},
	}

	s := test.NewSetup(t,
		test.WithConfig(testConfigJSON),
		test.WithPersistedServiceInfo("shared", srvInfoShared),
		test.WithPersistedServiceInfo("unshared", srvInfoUnshared),
		test.WithMockLiquidClient("shared", srvInfoShared),
		test.WithMockLiquidClient("unshared", srvInfoUnshared),
		test.WithInitialDiscovery,
	)
	tr, _ := easypg.NewTracker(t, s.DB.Db)

	// we try to do an impossible change now: unitNone to unitMebibytes
	resThings := srvInfoShared.Resources["things"]
	resThings.Unit = liquid.UnitMebibytes
	srvInfoShared.Resources["things"] = resThings
	s.LiquidClients["shared"].ServiceInfo.Set(srvInfoShared)

	errs := generateNewClusterWithPersistingServiceInfo(t, s, false)
	assert.Equal(t, len(errs), 1)
	assert.ErrEqual(t, errs[0], `failed to initialize service shared: saving ServiceInfo: cannot change unit of resource with id 2 from "" to "MiB", because the base units differ`)
	tr.DBChanges().AssertEmpty()

	// now we place some values in the database and convert from B to GiB
	sharedCapacityID := s.GetResourceID("shared", "capacity")
	sharedCapacityAZOneID := s.GetAZResourceID("shared", "capacity", "az-one")
	sharedCapacityTotalID := s.GetAZResourceID("shared", "capacity", "total")
	azResourceUpdate := `UPDATE az_resources SET raw_capacity = 7 * 1024 ^ 3, last_nonzero_raw_capacity = 7 * 1024 ^ 3, usage = 5 * 1024 ^ 3 WHERE id = $1`
	s.MustDBExec(azResourceUpdate, sharedCapacityAZOneID)
	s.MustDBExec(azResourceUpdate, sharedCapacityTotalID)
	s.MustDBInsert(&db.ProjectResource{
		ProjectID:                1,
		ResourceID:               sharedCapacityID,
		MaxQuotaFromOutsideAdmin: Some(uint64(10 * 1024 * 1024 * 1024)),
		OverrideQuotaFromConfig:  Some(uint64(5 * 1024 * 1024 * 1024)),
	})
	s.MustDBInsert(&db.ProjectAZResource{
		ProjectID:     1,
		AZResourceID:  sharedCapacityAZOneID,
		Quota:         Some(uint64(7 * 1024 * 1024 * 1024)),
		BackendQuota:  Some(int64(7 * 1024 * 1024 * 1024)),
		Usage:         uint64(5 * 1024 * 1024 * 1024),
		PhysicalUsage: Some(uint64(2 * 1024 * 1024 * 1024)),
	})
	s.MustDBInsert(&db.ProjectCommitment{
		UUID:                s.Collector.GenerateProjectCommitmentUUID(),
		ProjectID:           1,
		AZResourceID:        sharedCapacityAZOneID,
		Amount:              3 * uint64(1024*1024*1024),
		Duration:            must.Return(limesresources.ParseCommitmentDuration("10 days")),
		CreatedAt:           s.Clock.Now(),
		CreatorUUID:         "dummy",
		CreatorName:         "dummy",
		ConfirmedAt:         Some(s.Clock.Now()),
		CreationContextJSON: json.RawMessage(`{}`),
		ExpiresAt:           s.Clock.Now().Add(10 * 24 * time.Hour),
		Status:              "confirmed",
	})
	tr.DBChanges().Ignore()

	srvInfoShared = test.DefaultLiquidServiceInfo("Shared")
	resCapacity := srvInfoShared.Resources["capacity"]
	resCapacity.Unit = liquid.UnitGibibytes
	srvInfoShared.Resources["capacity"] = resCapacity
	s.LiquidClients["shared"].ServiceInfo.Set(srvInfoShared)
	errs = generateNewClusterWithPersistingServiceInfo(t, s, false)
	assert.Equal(t, len(errs), 0)
	tr.DBChanges().AssertEqual(`
		UPDATE az_resources SET raw_capacity = 7, usage = 5, last_nonzero_raw_capacity = 7 WHERE id = 2 AND resource_id = 1 AND az = 'az-one' AND path = 'shared/capacity/az-one';
		UPDATE az_resources SET raw_capacity = 7, usage = 5, last_nonzero_raw_capacity = 7 WHERE id = 4 AND resource_id = 1 AND az = 'total' AND path = 'shared/capacity/total';
		UPDATE project_az_resources SET quota = 7, usage = 5, physical_usage = 2, backend_quota = 7 WHERE id = 1 AND project_id = 1 AND az_resource_id = 2;
		UPDATE project_commitments SET amount = 3 WHERE id = 1 AND uuid = '00000000-0000-0000-0000-000000000001' AND transfer_token = NULL;
		UPDATE project_resources SET max_quota_from_outside_admin = 10, override_quota_from_config = 5 WHERE id = 1 AND project_id = 1 AND resource_id = 1;
		UPDATE resources SET unit = 'GiB' WHERE id = 1 AND service_id = 1 AND name = 'capacity' AND path = 'shared/capacity';
	`)

	// when we convert from GiB to MiB, this also works
	resCapacity.Unit = liquid.UnitMebibytes
	srvInfoShared.Resources["capacity"] = resCapacity
	s.LiquidClients["shared"].ServiceInfo.Set(srvInfoShared)
	errs = generateNewClusterWithPersistingServiceInfo(t, s, false)
	assert.Equal(t, len(errs), 0)
	tr.DBChanges().AssertEqual(`
		UPDATE az_resources SET raw_capacity = 7168, usage = 5120, last_nonzero_raw_capacity = 7168 WHERE id = 2 AND resource_id = 1 AND az = 'az-one' AND path = 'shared/capacity/az-one';
		UPDATE az_resources SET raw_capacity = 7168, usage = 5120, last_nonzero_raw_capacity = 7168 WHERE id = 4 AND resource_id = 1 AND az = 'total' AND path = 'shared/capacity/total';
		UPDATE project_az_resources SET quota = 7168, usage = 5120, physical_usage = 2048, backend_quota = 7168 WHERE id = 1 AND project_id = 1 AND az_resource_id = 2;
		UPDATE project_commitments SET amount = 3072 WHERE id = 1 AND uuid = '00000000-0000-0000-0000-000000000001' AND transfer_token = NULL;
		UPDATE project_resources SET max_quota_from_outside_admin = 10240, override_quota_from_config = 5120 WHERE id = 1 AND project_id = 1 AND resource_id = 1;
		UPDATE resources SET unit = 'MiB' WHERE id = 1 AND service_id = 1 AND name = 'capacity' AND path = 'shared/capacity';
	`)

	// when we set a value that will be scraped to something not convertible it will be rounded and non-blocking
	s.MustDBExec(`UPDATE project_az_resources SET backend_quota = 7000 WHERE project_id = 1 AND az_resource_id = $1`, sharedCapacityAZOneID)
	tr.DBChanges().Ignore()

	resCapacity.Unit = liquid.UnitGibibytes
	srvInfoShared.Resources["capacity"] = resCapacity
	s.LiquidClients["shared"].ServiceInfo.Set(srvInfoShared)
	errs = generateNewClusterWithPersistingServiceInfo(t, s, false)
	assert.Equal(t, len(errs), 0)
	tr.DBChanges().AssertEqual(`
		UPDATE az_resources SET raw_capacity = 7, usage = 5, last_nonzero_raw_capacity = 7 WHERE id = 2 AND resource_id = 1 AND az = 'az-one' AND path = 'shared/capacity/az-one';
		UPDATE az_resources SET raw_capacity = 7, usage = 5, last_nonzero_raw_capacity = 7 WHERE id = 4 AND resource_id = 1 AND az = 'total' AND path = 'shared/capacity/total';
		UPDATE project_az_resources SET quota = 7, usage = 5, physical_usage = 2, backend_quota = 6 WHERE id = 1 AND project_id = 1 AND az_resource_id = 2;
		UPDATE project_commitments SET amount = 3 WHERE id = 1 AND uuid = '00000000-0000-0000-0000-000000000001' AND transfer_token = NULL;
		UPDATE project_resources SET max_quota_from_outside_admin = 10, override_quota_from_config = 5 WHERE id = 1 AND project_id = 1 AND resource_id = 1;
		UPDATE resources SET unit = 'GiB' WHERE id = 1 AND service_id = 1 AND name = 'capacity' AND path = 'shared/capacity';
	`)

	// go back to MiB for next test
	resCapacity.Unit = liquid.UnitMebibytes
	srvInfoShared.Resources["capacity"] = resCapacity
	s.LiquidClients["shared"].ServiceInfo.Set(srvInfoShared)
	generateNewClusterWithPersistingServiceInfo(t, s, false)

	// now we set a non-convertible value for a commitment, this will block
	s.MustDBExec(`UPDATE project_commitments SET amount = 3000`)
	tr.DBChanges().Ignore()

	resCapacity.Unit = liquid.UnitGibibytes
	srvInfoShared.Resources["capacity"] = resCapacity
	s.LiquidClients["shared"].ServiceInfo.Set(srvInfoShared)
	errs = generateNewClusterWithPersistingServiceInfo(t, s, false)
	assert.Equal(t, len(errs), 1)
	assert.ErrEqual(t, errs[0], `failed to initialize service shared: saving ServiceInfo: there are 1 commitments with rounding issues when updating unit on resource_id 1 from "MiB" to "GiB"`)
	tr.DBChanges().AssertEmpty()

	// bring current db situation in sync with config again
	resCapacity.Unit = liquid.UnitMebibytes
	srvInfoShared.Resources["capacity"] = resCapacity
	s.LiquidClients["shared"].ServiceInfo.Set(srvInfoShared)

	// we try to do an impossible change for rates: unitMebibytes to unitNone
	rateOnlyInLiquid := srvInfoUnshared.Rates["only_in_liquid"]
	rateOnlyInLiquid.Unit = liquid.UnitNone
	srvInfoUnshared.Rates["only_in_liquid"] = rateOnlyInLiquid
	s.LiquidClients["unshared"].ServiceInfo.Set(srvInfoUnshared)

	errs = generateNewClusterWithPersistingServiceInfo(t, s, false)
	assert.Equal(t, len(errs), 1)
	assert.ErrEqual(t, errs[0], `failed to initialize service unshared: saving ServiceInfo: cannot change unit of rate with id 5 from "MiB" to "", because the base units differ`)
	tr.DBChanges().AssertEmpty()

	// now, we enter some values and do a conversion
	rateOnlyInLiquid.Unit = liquid.UnitGibibytes
	srvInfoUnshared.Rates["only_in_liquid"] = rateOnlyInLiquid
	s.LiquidClients["unshared"].ServiceInfo.Set(srvInfoUnshared)
	unsharedRateOnlyInLiquidID := s.GetRateID("unshared", "only_in_liquid")
	s.MustDBInsert(&db.ProjectRate{
		ProjectID:     1,
		RateID:        unsharedRateOnlyInLiquidID,
		Limit:         Some(uint64(10 * 1024)),
		Window:        Some(must.Return(limesrates.ParseWindow("1m"))),
		UsageAsBigint: "5120",
	})
	tr.DBChanges().Ignore()

	errs = generateNewClusterWithPersistingServiceInfo(t, s, false)
	assert.Equal(t, len(errs), 0)
	tr.DBChanges().AssertEqual(`
		UPDATE project_rates SET rate_limit = 10, usage_as_bigint = '5' WHERE id = 1 AND project_id = 1 AND rate_id = 5;
		UPDATE rates SET unit = 'GiB' WHERE id = 5 AND service_id = 2 AND name = 'only_in_liquid' AND path = 'unshared/only_in_liquid';
	`)

	// check that rounding is ignored for rates also
	s.MustDBExec(`UPDATE project_rates SET rate_limit = 1234, usage_as_bigint = '1234'`)
	rateOnlyInLiquid.Unit = liquid.UnitTebibytes
	srvInfoUnshared.Rates["only_in_liquid"] = rateOnlyInLiquid
	s.LiquidClients["unshared"].ServiceInfo.Set(srvInfoUnshared)

	errs = generateNewClusterWithPersistingServiceInfo(t, s, false)
	assert.Equal(t, len(errs), 0)
	tr.DBChanges().AssertEqual(`
		UPDATE project_rates SET rate_limit = 1, usage_as_bigint = '1' WHERE id = 1 AND project_id = 1 AND rate_id = 5;
		UPDATE rates SET unit = 'TiB' WHERE id = 5 AND service_id = 2 AND name = 'only_in_liquid' AND path = 'unshared/only_in_liquid';
	`)
}

func Test_ClusterServiceInfoRateLimitsUnitIncompatible(t *testing.T) {
	// we want to test that this crashes, so we spawn a subprocess which can crash
	if os.Getenv("TO_CRASH") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=Test_ClusterServiceInfoRateLimitsUnitIncompatible") //nolint:gosec // G204: this is intentional in tests
		cmd.Env = append(os.Environ(), "TO_CRASH=1")
		err := cmd.Run()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return
		}
		t.Fatalf("process ran with err %v, want exit status 1", err)
	}

	// actual test
	srvInfoShared := test.DefaultLiquidServiceInfo("Shared")
	srvInfoUnshared := test.DefaultLiquidServiceInfo("Unshared")
	srvInfoUnshared.Rates = map[liquid.RateName]liquid.RateInfo{
		"in_global_and_liquid":  {Unit: liquid.UnitMebibytes, Topology: liquid.FlatTopology, HasUsage: true},
		"in_liquid_and_project": {Unit: liquid.UnitMebibytes, Topology: liquid.FlatTopology, HasUsage: true},
		"only_in_liquid":        {Unit: liquid.UnitMebibytes, Topology: liquid.FlatTopology, HasUsage: true},
	}

	s := test.NewSetup(t,
		test.WithConfig(testConfigJSON),
		test.WithPersistedServiceInfo("shared", srvInfoShared),
		test.WithPersistedServiceInfo("unshared", srvInfoUnshared),
		test.WithMockLiquidClient("shared", srvInfoShared),
		test.WithMockLiquidClient("unshared", srvInfoUnshared),
		test.WithInitialDiscovery,
	)

	// rate updates should fail, when units are unequal between config and liquid
	rateInGlobalAndLiquid := srvInfoUnshared.Rates["in_global_and_liquid"]
	rateInGlobalAndLiquid.Unit = liquid.UnitGibibytes // config has MiB
	srvInfoUnshared.Rates["in_global_and_liquid"] = rateInGlobalAndLiquid
	s.LiquidClients["unshared"].ServiceInfo.Set(srvInfoUnshared)

	generateNewClusterWithPersistingServiceInfo(t, s, false)
}
