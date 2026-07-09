// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package core_test

import (
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/errext"
	"github.com/sapcc/go-bits/httptest"
	"github.com/sapcc/go-bits/must"
	"go.xyrillian.de/gg/assert"
	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/test"
	"github.com/sapcc/limes/internal/test/common_fixtures"
)

var testConfigJSON = string(must.Return(httptest.NewJQModifiableJSONString(`
	{
		"liquids": {
			"shared": {
				"area": "shared"
			},
			"unshared": {
				"area": "unshared",
				"rate_limits": {
					"global": [
						{"name": "with_global_limit", "limit": 10, "window": "1m", "unit": "MiB"}
					],
					"project_default": [
						{"name": "with_project_limit", "limit": 5, "window": "1m", "unit": "piece"}
					]
				}
			}
		}
	}`, "testConfigJSON").
	ModifyWithVariable(".areas = $ref", common_fixtures.AreasSharedUnshared).
	ModifyWithVariable(".availability_zones = $ref", common_fixtures.AZsOneTwo).
	ModifyWithVariable(".discovery = $ref", common_fixtures.DiscoveryBerlinDresdenParis).
	Modify("del(.discovery.static_config.domains[1])", `del(.discovery.static_config.projects["uuid-for-france"])`).
	MarshalJSON()))

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
		"only_usage":         {Unit: liquid.UnitMebibytes, Topology: liquid.FlatTopology, HasUsage: true, Category: Some(liquid.CategoryName("foo_category"))},
		"with_global_limit":  {Unit: liquid.UnitMebibytes, Topology: liquid.FlatTopology, HasUsage: false},
		"with_project_limit": {Unit: liquid.UnitNone, Topology: liquid.FlatTopology, HasUsage: false}, //nolint:staticcheck // intentionally using deprecated name UnitNone to validate the automatic rewrite into UnitPiece
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
		INSERT INTO rates (id, service_id, name, liquid_version, unit, topology, has_usage, path, category_id) VALUES (1, 2, 'only_usage', 1, 'MiB', 'flat', TRUE, 'unshared/only_usage', 1);
		INSERT INTO rates (id, service_id, name, liquid_version, unit, topology, path) VALUES (2, 2, 'with_global_limit', 1, 'MiB', 'flat', 'unshared/with_global_limit');
		INSERT INTO rates (id, service_id, name, liquid_version, unit, topology, path) VALUES (3, 2, 'with_project_limit', 1, 'piece', 'flat', 'unshared/with_project_limit');
		INSERT INTO resources (id, service_id, name, liquid_version, unit, topology, has_capacity, needs_resource_demand, has_quota, path, display_name, category_id) VALUES (3, 2, 'capacity', 1, 'B', 'az-aware', TRUE, TRUE, TRUE, 'unshared/capacity', 'Capacity', 1);
		INSERT INTO resources (id, service_id, name, liquid_version, unit, topology, has_quota, path, display_name) VALUES (4, 2, 'things', 1, 'piece', 'flat', TRUE, 'unshared/things', 'Things');
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
		INSERT INTO resources (id, service_id, name, liquid_version, unit, topology, has_quota, path, display_name) VALUES (5, 1, 'things', 3, 'piece', 'flat', TRUE, 'shared/things', 'Things');
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
	srvInfoUnshared.Rates["only_usage"] = liquid.RateInfo{Unit: liquid.UnitMebibytes, Topology: liquid.FlatTopology, HasUsage: true, Category: Some(liquid.CategoryName("bar_category"))}
	srvInfoUnshared.Rates["with_global_limit"] = liquid.RateInfo{Unit: liquid.UnitMebibytes, Topology: liquid.FlatTopology, HasUsage: false, Category: Some(liquid.CategoryName("foo_category"))}
	srvInfoUnshared.Version = 2
	s.LiquidClients["unshared"].ServiceInfo.Set(srvInfoUnshared)
	generateNewClusterWithPersistingServiceInfo(t, s, true)
	tr.DBChanges().AssertEqual(`
		INSERT INTO categories (id, name, display_name) VALUES (2, 'bar_category', 'Foo Category');
		UPDATE rates SET liquid_version = 2, category_id = 2 WHERE id = 1 AND service_id = 2 AND name = 'only_usage' AND path = 'unshared/only_usage';
		UPDATE rates SET liquid_version = 2, category_id = 1 WHERE id = 2 AND service_id = 2 AND name = 'with_global_limit' AND path = 'unshared/with_global_limit';
		UPDATE rates SET liquid_version = 2 WHERE id = 3 AND service_id = 2 AND name = 'with_project_limit' AND path = 'unshared/with_project_limit';
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
		"only_usage":         {Unit: liquid.UnitMebibytes, Topology: liquid.FlatTopology, HasUsage: true},
		"with_global_limit":  {Unit: liquid.UnitMebibytes, Topology: liquid.FlatTopology, HasUsage: false},
		"with_project_limit": {Unit: liquid.UnitPiece, Topology: liquid.FlatTopology, HasUsage: false},
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
	assert.ErrEqual(t, errs[0], `failed to initialize service shared: saving ServiceInfo: cannot change unit of resource with id 2 from "piece" to "MiB", because the base units differ`)
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
	tr.DBChanges().AssertEqualf(`
		UPDATE az_resources SET raw_capacity = 7, usage = 5, last_nonzero_raw_capacity = 7 WHERE id = 2 AND resource_id = 1 AND az = 'az-one' AND path = 'shared/capacity/az-one';
		UPDATE az_resources SET raw_capacity = 7, usage = 5, last_nonzero_raw_capacity = 7 WHERE id = 4 AND resource_id = 1 AND az = 'total' AND path = 'shared/capacity/total';
		UPDATE project_az_resources SET quota = 7, usage = 5, physical_usage = 2, backend_quota = 7 WHERE id = 1 AND project_id = 1 AND az_resource_id = 2;
		UPDATE project_commitments SET amount = 3, updated_at = %[1]d WHERE id = 1 AND uuid = '00000000-0000-0000-0000-000000000001' AND transfer_token = NULL;
		UPDATE project_resources SET max_quota_from_outside_admin = 10, override_quota_from_config = 5 WHERE id = 1 AND project_id = 1 AND resource_id = 1;
		UPDATE resources SET unit = 'GiB' WHERE id = 1 AND service_id = 1 AND name = 'capacity' AND path = 'shared/capacity';
	`, s.Clock.Now().Unix())

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

	// we try to do an impossible change for rates: unitMebibytes to unitPiece
	rateOnlyUsage := srvInfoUnshared.Rates["only_usage"]
	rateOnlyUsage.Unit = liquid.UnitPiece
	srvInfoUnshared.Rates["only_usage"] = rateOnlyUsage
	s.LiquidClients["unshared"].ServiceInfo.Set(srvInfoUnshared)

	errs = generateNewClusterWithPersistingServiceInfo(t, s, false)
	assert.Equal(t, len(errs), 1)
	assert.ErrEqual(t, errs[0], `failed to initialize service unshared: saving ServiceInfo: cannot change unit of rate with id 1 from "MiB" to "piece", because the base units differ`)
	tr.DBChanges().AssertEmpty()

	// now, we enter some values and do a conversion
	rateOnlyUsage.Unit = liquid.UnitGibibytes
	srvInfoUnshared.Rates["only_usage"] = rateOnlyUsage
	s.LiquidClients["unshared"].ServiceInfo.Set(srvInfoUnshared)
	unsharedRateOnlyUsageID := s.GetRateID("unshared", "only_usage")
	s.MustDBInsert(&db.ProjectRate{
		ProjectID:     1,
		RateID:        unsharedRateOnlyUsageID,
		Limit:         Some(uint64(10 * 1024)),
		Window:        Some(must.Return(limesrates.ParseWindow("1m"))),
		UsageAsBigint: "5120",
	})
	tr.DBChanges().Ignore()

	errs = generateNewClusterWithPersistingServiceInfo(t, s, false)
	assert.Equal(t, len(errs), 0)
	tr.DBChanges().AssertEqualf(`
		UPDATE project_rates SET rate_limit = 10, usage_as_bigint = '5' WHERE id = 1 AND project_id = 1 AND rate_id = %[1]d;
		UPDATE rates SET unit = 'GiB' WHERE id = %[1]d AND service_id = 2 AND name = 'only_usage' AND path = 'unshared/only_usage';
	`, unsharedRateOnlyUsageID)

	// check that rounding is ignored for rates also
	s.MustDBExec(`UPDATE project_rates SET rate_limit = 1234, usage_as_bigint = '1234'`)
	rateOnlyUsage.Unit = liquid.UnitTebibytes
	srvInfoUnshared.Rates["only_usage"] = rateOnlyUsage
	s.LiquidClients["unshared"].ServiceInfo.Set(srvInfoUnshared)

	errs = generateNewClusterWithPersistingServiceInfo(t, s, false)
	assert.Equal(t, len(errs), 0)
	tr.DBChanges().AssertEqualf(`
		UPDATE project_rates SET rate_limit = 1, usage_as_bigint = '1' WHERE id = 1 AND project_id = 1 AND rate_id = %[1]d;
		UPDATE rates SET unit = 'TiB' WHERE id = %[1]d AND service_id = 2 AND name = 'only_usage' AND path = 'unshared/only_usage';
	`, unsharedRateOnlyUsageID)
}

func Test_ClusterServiceInfoRateLimitsUnitIncompatible(t *testing.T) {
	srvInfoShared := test.DefaultLiquidServiceInfo("Shared")
	srvInfoUnshared := test.DefaultLiquidServiceInfo("Unshared")
	srvInfoUnshared.Rates = map[liquid.RateName]liquid.RateInfo{
		"only_usage":         {Unit: liquid.UnitMebibytes, Topology: liquid.FlatTopology, HasUsage: true},
		"with_global_limit":  {Unit: liquid.UnitMebibytes, Topology: liquid.FlatTopology, HasUsage: false},
		"with_project_limit": {Unit: liquid.UnitPiece, Topology: liquid.FlatTopology, HasUsage: false},
	}

	s := test.NewSetup(t,
		test.WithConfig(testConfigJSON),
		test.WithPersistedServiceInfo("shared", srvInfoShared),
		test.WithPersistedServiceInfo("unshared", srvInfoUnshared),
		test.WithMockLiquidClient("shared", srvInfoShared),
		test.WithMockLiquidClient("unshared", srvInfoUnshared),
		test.WithInitialDiscovery,
	)

	// rate updates should fail when units are unequal between config and liquid
	rateWithGlobalLimit := srvInfoUnshared.Rates["with_global_limit"]
	rateWithGlobalLimit.Unit = liquid.UnitGibibytes // config has MiB
	srvInfoUnshared.Rates["with_global_limit"] = rateWithGlobalLimit
	s.LiquidClients["unshared"].ServiceInfo.Set(srvInfoUnshared)

	errs := generateNewClusterWithPersistingServiceInfo(t, s, false)
	assert.Equal(t, len(errs), 1)
	assert.ErrEqual(t, errs[0], `failed to initialize service unshared: saving ServiceInfo: configuration uses unit "MiB" for rate unshared/with_global_limit, but liquid declared unit "GiB"`)
}
