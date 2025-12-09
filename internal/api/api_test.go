// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/go-gorp/gorp/v3"
	"github.com/gofrs/uuid/v5"
	. "github.com/majewsky/gg/option"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/must"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/test"
)

func TestMain(m *testing.M) {
	easypg.WithTestDB(m, func() int { return m.Run() })
}

// NOTE: MiB makes no sense for a deletion rate, but I want to test as many
// combinations of "has unit or not", "has limit or not" and "has usage or not"
// as possible
const (
	testConfigJSON = `{
		"availability_zones": ["az-one", "az-two"],
		"discovery": {
			"method": "static",
			"static_config": {
				"domains": [
					{"name": "germany", "id": "uuid-for-germany"},
					{"name": "france", "id": "uuid-for-france"}
				],
				"projects": {
					"uuid-for-germany": [
						{"name": "berlin", "id": "uuid-for-berlin", "parent_id": "uuid-for-germany"},
						{"name": "dresden", "id": "uuid-for-dresden", "parent_id": "uuid-for-berlin"}
					],
					"uuid-for-france": [
						{"name": "paris", "id": "uuid-for-paris", "parent_id": "uuid-for-france"}
					]
				}
			}
		},
		"liquids": {
			"shared": {
				"area": "shared",
				"rate_limits": {
					"global": [
						{"name": "service/shared/objects:create", "limit": 5000, "window": "1s"}
					],
					"project_default": [
						{"name": "service/shared/objects:create", "limit": 5, "window": "1m"},
						{"name": "service/shared/objects:delete", "limit": 1, "window": "1m"},
						{"name": "service/shared/objects:update", "limit": 2, "window": "1s"},
						{"name": "service/shared/objects:read/list", "limit": 3, "window": "1s"}
					]
				},
				"commitment_behavior_per_resource": [
					{
						"key": "capacity|things",
						"value": {
							"durations_per_domain": [{"key": ".+", "value": ["1 hour", "2 hours"]}],
							"min_confirm_date": "1970-01-08T00:00:00Z" // one week after start of mock.Clock
						}
					}
				]
			},
			"unshared": {
				"area": "unshared",
				"rate_limits": {
					"project_default": [
						{"name": "service/unshared/instances:create", "limit": 5, "window": "1m"},
						{"name": "service/unshared/instances:delete", "limit": 1, "window": "1m"},
						{"name": "service/unshared/instances:update", "limit": 2, "window": "1s"}
					]
				}
			}
		},
		"resource_behavior": [
			{
				// check that category mapping is reported
				"resource": ".+/capacity_az_separated",
				"category": "foo_category"
			}
		]
	}`
)

func setupTest(t *testing.T) test.Setup {
	// NOTE: For new tests, please try to use a more minimal setup that focuses on the specific needs of the test.
	//       This test setup is designed to be backwards-compatible with the old start-data.sql fixture.

	srvInfoShared := test.DefaultLiquidServiceInfo()
	srvInfoShared.Rates = map[liquid.RateName]liquid.RateInfo{
		"service/shared/objects:create":    {Topology: liquid.FlatTopology, HasUsage: true},
		"service/shared/objects:delete":    {Unit: liquid.UnitMebibytes, Topology: liquid.FlatTopology, HasUsage: true},
		"service/shared/objects:update":    {Topology: liquid.FlatTopology, HasUsage: true},
		"service/shared/objects:unlimited": {Unit: liquid.UnitKibibytes, Topology: liquid.FlatTopology, HasUsage: true},
	}
	srvInfoUnshared := test.DefaultLiquidServiceInfo()
	srvInfoUnshared.Rates = map[liquid.RateName]liquid.RateInfo{
		"service/unshared/instances:create": {Topology: liquid.FlatTopology, HasUsage: true},
		"service/unshared/instances:delete": {Topology: liquid.FlatTopology, HasUsage: true},
		"service/unshared/instances:update": {Topology: liquid.FlatTopology, HasUsage: true},
	}
	s := test.NewSetup(t,
		test.WithConfig(testConfigJSON),
		test.WithPersistedServiceInfo("shared", srvInfoShared),
		test.WithPersistedServiceInfo("unshared", srvInfoUnshared),
		test.WithInitialDiscovery,
		test.WithEmptyRecordsAsNeeded,
	)

	// shorthands
	unix := func(val int64) time.Time { return time.Unix(val, 0) }

	berlin := s.GetProjectID("berlin")
	dresden := s.GetProjectID("dresden")
	paris := s.GetProjectID("paris")

	shared := s.GetServiceID("shared")
	unshared := s.GetServiceID("unshared")

	sharedCapacity := s.GetResourceID("shared", "capacity")

	sharedCapacityAny := s.GetAZResourceID("shared", "capacity", limes.AvailabilityZoneAny)
	sharedCapacityAZOne := s.GetAZResourceID("shared", "capacity", "az-one")
	sharedCapacityAZTwo := s.GetAZResourceID("shared", "capacity", "az-two")
	sharedCapacityTotal := s.GetAZResourceID("shared", "capacity", liquid.AvailabilityZoneTotal)
	sharedThingsAny := s.GetAZResourceID("shared", "things", limes.AvailabilityZoneAny)
	sharedThingsTotal := s.GetAZResourceID("shared", "things", liquid.AvailabilityZoneTotal)
	unsharedCapacityAny := s.GetAZResourceID("unshared", "capacity", limes.AvailabilityZoneAny)
	unsharedCapacityAZOne := s.GetAZResourceID("unshared", "capacity", "az-one")
	unsharedCapacityAZTwo := s.GetAZResourceID("unshared", "capacity", "az-two")
	unsharedCapacityTotal := s.GetAZResourceID("unshared", "capacity", liquid.AvailabilityZoneTotal)
	unsharedThingsAny := s.GetAZResourceID("unshared", "things", limes.AvailabilityZoneAny)
	unsharedThingsTotal := s.GetAZResourceID("unshared", "things", liquid.AvailabilityZoneTotal)

	sharedObjectsCreate := s.GetRateID("shared", "service/shared/objects:create")
	sharedObjectsDelete := s.GetRateID("shared", "service/shared/objects:delete")
	sharedObjectsUpdate := s.GetRateID("shared", "service/shared/objects:update")
	sharedObjectsUnlimited := s.GetRateID("shared", "service/shared/objects:unlimited")
	unsharedInstancesCreate := s.GetRateID("unshared", "service/unshared/instances:create")
	unsharedInstancesDelete := s.GetRateID("unshared", "service/unshared/instances:delete")
	unsharedInstancesUpdate := s.GetRateID("unshared", "service/unshared/instances:update")

	// fill `services`
	query := `UPDATE services SET scraped_at = $1, next_scrape_at = $2 WHERE type = $3`
	s.MustDBExec(query, unix(1000), unix(2000), "unshared")
	s.MustDBExec(query, unix(1100), unix(2100), "shared")

	// fill `az_resources` (unshared/capacity has zero capacity)
	query = `UPDATE az_resources SET raw_capacity = $1, last_nonzero_raw_capacity = $1, usage = $2, subcapacities = $3 WHERE path = $4`
	s.MustDBExec(query, 139, 45, `[{"smaller_half":46},{"larger_half":93}]`, "unshared/things/any")
	s.MustDBExec(query, 139, 45, `[{"smaller_half":46},{"larger_half":93}]`, "unshared/things/total")
	s.MustDBExec(query, 246, 158, `[{"smaller_half":82},{"larger_half":164}]`, "shared/things/any")
	s.MustDBExec(query, 246, 158, `[{"smaller_half":82},{"larger_half":164}]`, "shared/things/total")
	s.MustDBExec(query, 90, 12, "", "shared/capacity/az-one")
	s.MustDBExec(query, 95, 15, "", "shared/capacity/az-two")
	s.MustDBExec(query, 185, 27, "", "shared/capacity/total")

	// fill `project_services`
	query = `UPDATE project_services SET stale = FALSE, scraped_at = $1, checked_at = $1 WHERE project_id = $2 AND service_id = $3`
	s.MustDBExec(query, unix(11), berlin, unshared)
	s.MustDBExec(query, unix(22), berlin, shared)
	s.MustDBExec(query, unix(33), dresden, unshared)
	s.MustDBExec(query, unix(44), dresden, shared)
	s.MustDBExec(query, unix(55), paris, unshared)
	s.MustDBExec(query, unix(66), paris, shared)

	// fill `project_resources` (most have quota = 10, some test special cases)
	s.MustDBExec(`UPDATE project_az_resources SET quota = 10, backend_quota = 10 WHERE az_resource_id IN (SELECT id FROM az_resources WHERE az = $1)`, liquid.AvailabilityZoneTotal)
	s.MustDBExec(`UPDATE project_az_resources SET backend_quota = 100 WHERE project_id = $1 AND az_resource_id = $2`, dresden, sharedCapacityTotal)
	s.MustDBExec(`UPDATE project_az_resources SET backend_quota = -1 WHERE project_id = $1 AND az_resource_id = $2`, paris, unsharedThingsTotal)
	s.MustDBExec(`UPDATE project_resources SET max_quota_from_outside_admin = 200 WHERE project_id = $1 AND resource_id = $2`, paris, sharedCapacity)

	// fill `project_az_resources` subresources (only in Berlin)
	query = `UPDATE project_az_resources SET subresources = $1 WHERE project_id = $2 AND az_resource_id = $3`
	s.MustDBExec(query, `[{"id":"firstthing","value":23},{"id":"secondthing","value":42}]`, berlin, unsharedThingsAny)
	s.MustDBExec(query, `[{"id":"firstthing","value":23},{"id":"secondthing","value":42}]`, berlin, unsharedThingsTotal)
	s.MustDBExec(query, `[{"id":"thirdthing","value":5},{"id":"fourththing","value":123}]`, berlin, sharedThingsAny)
	s.MustDBExec(query, `[{"id":"thirdthing","value":5},{"id":"fourththing","value":123}]`, berlin, sharedThingsTotal)

	// fill `project_az_resources` usage:
	// - every resource has usage = 2 per project, but split between AZs for "capacity"
	// - for Berlin and "things", this matches the subresource count above
	query = `UPDATE project_az_resources SET usage = $1 WHERE az_resource_id = $2`
	s.MustDBExec(query, 1, sharedCapacityAZOne)
	s.MustDBExec(query, 1, sharedCapacityAZTwo)
	s.MustDBExec(query, 2, sharedCapacityTotal)
	s.MustDBExec(query, 2, sharedThingsAny)
	s.MustDBExec(query, 2, sharedThingsTotal)
	s.MustDBExec(query, 1, unsharedCapacityAZOne)
	s.MustDBExec(query, 1, unsharedCapacityAZTwo)
	s.MustDBExec(query, 2, unsharedCapacityTotal)
	s.MustDBExec(query, 2, unsharedThingsAny)
	s.MustDBExec(query, 2, unsharedThingsTotal)

	// fill `project_az_resources` quota:
	// - in most cases, the even quota of 10 from above gets split evenly between relevant AZs
	// - Dresden instead has some quota shifted into `any` for the AZ-aware resources (2x3 AZ-aware + 4 any = 10)
	// - Paris does not have quota at all (aggregation should only count quota in projects that have it)
	query = `UPDATE project_az_resources SET quota = $1 WHERE az_resource_id = $2`
	s.MustDBExec(query, 0, sharedCapacityAny)
	s.MustDBExec(query, 5, sharedCapacityAZOne)
	s.MustDBExec(query, 5, sharedCapacityAZTwo)
	s.MustDBExec(query, 10, sharedThingsAny)
	s.MustDBExec(query, 0, unsharedCapacityAny)
	s.MustDBExec(query, 5, unsharedCapacityAZOne)
	s.MustDBExec(query, 5, unsharedCapacityAZTwo)
	s.MustDBExec(query, 10, unsharedThingsAny)
	query = `UPDATE project_az_resources SET quota = $1 WHERE project_id = $2 AND az_resource_id = $3`
	s.MustDBExec(query, 4, dresden, sharedCapacityAny)
	s.MustDBExec(query, 3, dresden, sharedCapacityAZOne)
	s.MustDBExec(query, 3, dresden, sharedCapacityAZTwo)
	s.MustDBExec(query, 10, dresden, sharedCapacityTotal)
	s.MustDBExec(query, 4, dresden, unsharedCapacityAny)
	s.MustDBExec(query, 3, dresden, unsharedCapacityAZOne)
	s.MustDBExec(query, 3, dresden, unsharedCapacityAZTwo)
	s.MustDBExec(query, 10, dresden, unsharedCapacityTotal)
	// TODO: what situation did the following line simulate?
	//   I know it should create the situation from L208, but this did not modify project_resources, so it was counted in with the old logic.
	// s.MustDBExec(`UPDATE project_az_resources SET quota = NULL WHERE project_id = $1`, paris)

	// fill `project_az_resources` physical usage: only Paris (aggregation should consider physical_usage = usage in the other projects)
	query = `UPDATE project_az_resources SET physical_usage = $1 WHERE project_id = $2 AND az_resource_id = $3`
	s.MustDBExec(query, 0, paris, sharedCapacityAZOne)
	s.MustDBExec(query, 1, paris, sharedCapacityAZTwo)
	s.MustDBExec(query, 1, paris, sharedCapacityTotal)
	s.MustDBExec(query, 0, paris, unsharedCapacityAZOne)
	s.MustDBExec(query, 1, paris, unsharedCapacityAZTwo)
	s.MustDBExec(query, 1, paris, unsharedCapacityTotal)

	// fill `project_rates`:
	// - Berlin has custom rate limits
	// - only Dresden has usage values, and it also shows usage for a rate that does not have rate limits
	// - Dresden also has zero-valued usage values, which is different from empty string (empty string means "usage unknown", 0 means "no usage yet")
	// - Paris has no records at all, so the API will only display the default rate limits
	window := Some(1 * limesrates.WindowMinutes)
	s.MustDBInsert(&db.ProjectRate{ProjectID: berlin, RateID: unsharedInstancesCreate, Limit: Some[uint64](5), Window: window})
	s.MustDBInsert(&db.ProjectRate{ProjectID: berlin, RateID: unsharedInstancesDelete, Limit: Some[uint64](2), Window: window, UsageAsBigint: "12345"})
	s.MustDBInsert(&db.ProjectRate{ProjectID: berlin, RateID: unsharedInstancesUpdate, Limit: Some[uint64](2), Window: window})
	s.MustDBInsert(&db.ProjectRate{ProjectID: berlin, RateID: sharedObjectsCreate, Limit: Some[uint64](5), Window: window})
	s.MustDBInsert(&db.ProjectRate{ProjectID: berlin, RateID: sharedObjectsDelete, Limit: Some[uint64](2), Window: window, UsageAsBigint: "23456"})
	s.MustDBInsert(&db.ProjectRate{ProjectID: berlin, RateID: sharedObjectsUpdate, Limit: Some[uint64](2), Window: window})
	s.MustDBInsert(&db.ProjectRate{ProjectID: dresden, RateID: unsharedInstancesDelete, UsageAsBigint: "0"})
	s.MustDBInsert(&db.ProjectRate{ProjectID: dresden, RateID: sharedObjectsDelete, UsageAsBigint: "0"})
	s.MustDBInsert(&db.ProjectRate{ProjectID: dresden, RateID: sharedObjectsUnlimited, UsageAsBigint: "1048576"})

	// fill `project_commitments`: we only really care about duration, status and amount;
	// this helper function fills most other relevant fields to look vaguely plausible
	makeCommitment := func(projectID db.ProjectID, azResourceID db.AZResourceID, amount uint64, status liquid.CommitmentStatus, durationStr string) *db.ProjectCommitment {
		duration := must.Return(limesresources.ParseCommitmentDuration(durationStr))
		c := db.ProjectCommitment{
			UUID:                liquid.CommitmentUUID(must.Return(uuid.NewV4()).String()),
			ProjectID:           projectID,
			AZResourceID:        azResourceID,
			Amount:              amount,
			Duration:            duration,
			Status:              status,
			CreatedAt:           s.Clock.Now(),
			CreatorUUID:         "uuid-for-alice",
			CreatorName:         "alice@Default",
			ExpiresAt:           duration.AddTo(s.Clock.Now()),
			CreationContextJSON: json.RawMessage(`{}`),
		}
		switch c.Status {
		case "pending":
			c.ConfirmBy = Some(s.Clock.Now())
		case "planned":
			c.ConfirmBy = Some(s.Clock.Now().Add(24 * time.Hour))
		}
		return &c
	}
	s.MustDBInsert(makeCommitment(dresden, unsharedCapacityAZOne, 1, "confirmed", "2 years"))
	s.MustDBInsert(makeCommitment(dresden, unsharedCapacityAZOne, 1, "confirmed", "1 year"))
	s.MustDBInsert(makeCommitment(dresden, unsharedCapacityAZOne, 1, "confirmed", "1 year"))
	s.MustDBInsert(makeCommitment(dresden, unsharedCapacityAZTwo, 2, "confirmed", "1 year"))
	s.MustDBInsert(makeCommitment(dresden, unsharedCapacityAZTwo, 100, "pending", "2 years"))
	s.MustDBInsert(makeCommitment(dresden, unsharedCapacityAZOne, 5, "expired", "10 minutes"))
	s.MustDBInsert(makeCommitment(dresden, sharedCapacityAZOne, 100, "planned", "2 years"))
	s.MustDBInsert(makeCommitment(dresden, unsharedThingsAny, 1, "confirmed", "2 years"))

	// all reports are pulled at the same simulated time, `s.Clock().Now().Unix() == 3600`,
	// to match the setup of confirmed vs. expired commitments above
	s.Clock.StepBy(1 * time.Hour)

	return s
}

func Test_ScrapeErrorOperations(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(`{
			"availability_zones": ["az-one", "az-two"],
			"discovery": {
				"method": "static",
				"static_config": {
					"domains": [{"name": "germany", "id": "uuid-for-germany"}],
					"projects": {
						"uuid-for-germany": [
							{"name": "berlin", "id": "uuid-for-berlin", "parent_id": "uuid-for-germany"},
							{"name": "dresden", "id": "uuid-for-dresden", "parent_id": "uuid-for-germany"}
						]
					}
				}
			},
			"liquids": {
				"shared": {"area": "shared"},
				"unshared": {"area": "unshared"}
			}
		}`),
		test.WithPersistedServiceInfo("shared", test.DefaultLiquidServiceInfo()),
		test.WithPersistedServiceInfo("unshared", test.DefaultLiquidServiceInfo()),
		test.WithInitialDiscovery,
		test.WithEmptyRecordsAsNeeded,
	)

	s.MustDBExec(`UPDATE project_services SET scraped_at = $1, checked_at = $1`, time.Unix(11, 0))

	// by default, there are no scrape errors to report
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/admin/scrape-errors",
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONObject{"scrape_errors": []assert.JSONObject{}},
	}.Check(t, s.Handler)

	// add a scrape error to one specific service with type 'unshared'.
	s.MustDBExec(
		`UPDATE project_services SET scrape_error_message = $1 WHERE project_id = 1 AND service_id = $2`,
		"could not scrape this specific unshared service",
		s.GetServiceID("unshared"),
	)

	// add the same scrape error to all services with type 'shared'. This will ensure that
	// they get grouped under a dummy project.
	s.MustDBExec(
		`UPDATE project_services SET scrape_error_message = $1 WHERE service_id = $2`,
		"could not scrape shared service",
		s.GetServiceID("shared"),
	)

	// check ListScrapeErrors
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/admin/scrape-errors",
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/scrape-error-list.json"),
	}.Check(t, s.Handler)
}

func Test_ClusterOperations(t *testing.T) {
	s := setupTest(t)

	// check GetCluster
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/clusters/current",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("fixtures/cluster-get-west.json"),
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/clusters/current?service=unknown",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("fixtures/cluster-get-west-no-services.json"),
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/clusters/current?service=shared&resource=unknown",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("fixtures/cluster-get-west-no-services.json"),
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/clusters/current?service=shared&resource=things",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("fixtures/cluster-get-west-filtered.json"),
	}.Check(t, s.Handler)

	// check GetCluster with new API features enabled
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/clusters/current",
		Header:       map[string]string{"X-Limes-V2-API-Preview": "per-az"},
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/cluster-get-west-with-v2-api.json"),
	}.Check(t, s.Handler)

	// check GetClusterRates
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/rates/v1/clusters/current",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("fixtures/cluster-get-west-only-rates.json"),
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/rates/v1/clusters/current?rates",
		ExpectStatus: 400,
		ExpectBody:   assert.StringData("the `rates` query parameter is not allowed here\n"),
	}.Check(t, s.Handler)

	// check rendering of overcommit factors
	s.Cluster.Config.ResourceBehaviors = []core.ResourceBehavior{
		{
			FullResourceNameRx: "shared/things",
			OvercommitFactor:   2.5,
		},
		{
			FullResourceNameRx: "unshared/things",
			OvercommitFactor:   1.5,
		},
	}
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/clusters/current",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("fixtures/cluster-get-west-with-overcommit.json"),
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/clusters/current",
		Header:       map[string]string{"X-Limes-V2-API-Preview": "per-az"},
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("fixtures/cluster-get-west-with-overcommit-and-v2-api.json"),
	}.Check(t, s.Handler)
}

func Test_DomainOperations(t *testing.T) {
	s := setupTest(t)
	discovery := s.Cluster.DiscoveryPlugin.(*core.StaticDiscoveryPlugin)

	// check GetDomain
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/domain-get-germany.json"),
	}.Check(t, s.Handler)
	// domain "france" covers some special cases: an infinite backend quota and
	// missing domain quota entries for one service
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-france",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/domain-get-france.json"),
	}.Check(t, s.Handler)

	// check ListDomains
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/domain-list.json"),
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains?service=unknown",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/domain-list-no-services.json"),
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains?service=shared&resource=unknown",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/domain-list-no-services.json"),
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains?service=shared&resource=things",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/domain-list-filtered.json"),
	}.Check(t, s.Handler)

	// check ListDomains with new API features enabled
	// TODO: Why did some of the "per_az" not have the quota? This query was not touched, so I don't understand why.
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains",
		Header:       map[string]string{"X-Limes-V2-API-Preview": "per-az"},
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/domain-list-with-v2-api.json"),
	}.Check(t, s.Handler)

	// check DiscoverDomains
	discovery.Config.Domains = append(discovery.Config.Domains,
		core.KeystoneDomain{Name: "spain", UUID: "uuid-for-spain"},
	)
	discovery.Config.Projects["uuid-for-spain"] = append(discovery.Config.Projects["uuid-for-spain"],
		core.KeystoneProject{UUID: "uuid-for-madrid", Name: "madrid", ParentUUID: "uuid-for-spain"},
	)
	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/discover",
		ExpectStatus: 202,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/domain-discover.json"),
	}.Check(t, s.Handler)

	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/discover",
		ExpectStatus: 204, // no content because no new domains discovered
		ExpectBody:   assert.StringData(""),
	}.Check(t, s.Handler)
}

func Test_ProjectOperations(t *testing.T) {
	s := setupTest(t)
	discovery := s.Cluster.DiscoveryPlugin.(*core.StaticDiscoveryPlugin)

	// check GetProject
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-get-berlin.json"),
	}.Check(t, s.Handler)
	// check rendering of subresources
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin?detail",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-get-details-berlin.json"),
	}.Check(t, s.Handler)
	// dresden has a case of backend quota != quota
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-dresden",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-get-dresden.json"),
	}.Check(t, s.Handler)
	// paris has a case of infinite backend quota
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-france/projects/uuid-for-paris",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-get-paris.json"),
	}.Check(t, s.Handler)

	// paris has forbid_autogrowth setting
	s.MustDBExec(
		"UPDATE project_resources SET forbid_autogrowth = true WHERE project_id = $1 AND resource_id = $2",
		s.GetProjectID("paris"),
		s.GetResourceID("shared", "capacity"),
	)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-france/projects/uuid-for-paris",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-get-paris-forbid-autogrowth.json"),
	}.Check(t, s.Handler)

	// check GetProjectRates
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/rates/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-get-berlin-only-rates.json"),
	}.Check(t, s.Handler)
	// dresden has some rates that only report usage
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/rates/v1/domains/uuid-for-germany/projects/uuid-for-dresden",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-get-dresden-only-rates.json"),
	}.Check(t, s.Handler)
	// paris has no rates in the DB whatsoever, so we can check the rendering of the default rates
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/rates/v1/domains/uuid-for-france/projects/uuid-for-paris",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-get-paris-only-default-rates.json"),
	}.Check(t, s.Handler)

	// check non-existent domains/projects
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-switzerland/projects/uuid-for-bern",
		ExpectStatus: 404,
		ExpectBody:   assert.StringData("no such domain (if it was just created, try to POST /domains/discover)\n"),
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-hamburg",
		ExpectStatus: 404,
		ExpectBody:   assert.StringData("no such project (if it was just created, try to POST /domains/uuid-for-germany/projects/discover)\n"),
	}.Check(t, s.Handler)

	// check ListProjects
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-list.json"),
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects?service=unknown",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-list-no-services.json"),
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects?service=shared&resource=unknown",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-list-no-services.json"),
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects?service=shared&resource=things",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-list-filtered.json"),
	}.Check(t, s.Handler)

	// check ListProjects with new API features enabled
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects",
		Header:       map[string]string{"X-Limes-V2-API-Preview": "per-az"},
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-list-with-v2-api.json"),
	}.Check(t, s.Handler)

	// check ListProjects does not report commitment duration if the forbidden flag is set
	s.MustDBExec(`UPDATE project_resources SET forbidden = TRUE`)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-list-forbidden.json"),
	}.Check(t, s.Handler)
	s.MustDBExec(`UPDATE project_resources SET forbidden = FALSE`)

	// check ListProjectRates
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/rates/v1/domains/uuid-for-germany/projects",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-list-only-rates.json"),
	}.Check(t, s.Handler)

	// check ?area= filter (esp. interaction with ?service= filter)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects?area=unknown",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-list-no-services.json"),
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects?area=shared&service=unshared",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-list-no-services.json"),
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects?area=shared&resource=things",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-list-filtered.json"),
	}.Check(t, s.Handler)

	// check DiscoverProjects
	discovery.Config.Projects["uuid-for-germany"] = append(discovery.Config.Projects["uuid-for-germany"],
		core.KeystoneProject{Name: "frankfurt", UUID: "uuid-for-frankfurt", ParentUUID: "uuid-for-germany"},
	)
	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/uuid-for-germany/projects/discover",
		ExpectStatus: 202,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-discover.json"),
	}.Check(t, s.Handler)

	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/uuid-for-germany/projects/discover",
		ExpectStatus: 204, // no content because no new projects discovered
		ExpectBody:   assert.StringData(""),
	}.Check(t, s.Handler)

	// DiscoverProjects sets `stale` on new project_services;
	// clear this to avoid confusion in the next test
	s.MustDBExec(`UPDATE project_services SET stale = FALSE WHERE project_id = $1`, s.GetProjectID("frankfurt"))

	// check SyncProject
	expectStaleProjectServices(t, s.DB /*, nothing */)
	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-dresden/sync",
		ExpectStatus: 202,
		ExpectBody:   assert.StringData(""),
	}.Check(t, s.Handler)
	expectStaleProjectServices(t, s.DB, "dresden:shared", "dresden:unshared")

	// SyncProject should discover the given project if not yet done
	discovery.Config.Projects["uuid-for-germany"] = append(discovery.Config.Projects["uuid-for-germany"],
		core.KeystoneProject{Name: "walldorf", UUID: "uuid-for-walldorf", ParentUUID: "uuid-for-germany"},
	)
	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-walldorf/sync",
		ExpectStatus: 202,
		ExpectBody:   assert.StringData(""),
	}.Check(t, s.Handler)
	expectStaleProjectServices(t, s.DB, "dresden:shared", "dresden:unshared", "walldorf:shared", "walldorf:unshared")

	// check GetProject for a project that has been discovered, but not yet synced
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-walldorf",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-get-walldorf-not-scraped-yet.json"),
	}.Check(t, s.Handler)

	// Check PUT ../project with rate limits.
	// Attempt setting a rate limit for which no default exists should fail.
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/rates/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 500, // TODO: should be 403 (I don't care about fixing this in v1; v2 will be structured differently to allow for a fix)
		ExpectBody: assert.StringData(
			"no such rate: shared/service/shared/notexistent:bogus\n",
		),
		Body: assert.JSONObject{
			"project": assert.JSONObject{
				"services": []assert.JSONObject{
					{
						"type": "shared",
						"rates": []assert.JSONObject{
							{
								"name":   "service/shared/notexistent:bogus",
								"limit":  1,
								"window": "1h",
							},
						},
					},
				},
			},
		},
	}.Check(t, s.Handler)
	var (
		actualLimit   uint64
		actualWindow  limesrates.Window
		projectRateId db.ProjectRateID
	)
	err := s.DB.QueryRow(`
		SELECT pra.rate_limit, pra.window_ns
		FROM project_rates pra
		JOIN rates ra ON ra.id = pra.rate_id
		JOIN services s ON s.id = ra.service_id
		JOIN projects p ON p.id = pra.project_id
		WHERE p.name = $1 AND s.type = $2 AND ra.name = $3`,
		"berlin", "shared", "service/shared/notexistent:bogus").Scan(&actualLimit, &actualWindow)
	// There shouldn't be anything in the DB.
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected error %v but got %v", sql.ErrNoRows, err)
	}

	// Attempt setting a rate limit for which a default exists should be successful.
	rateName := "service/shared/objects:read/list"
	expectedLimit := uint64(100)
	expectedWindow := 1 * limesrates.WindowSeconds
	makeRequest := func(name string, limit uint64, window limesrates.Window) assert.JSONObject {
		return assert.JSONObject{
			"project": assert.JSONObject{
				"services": []assert.JSONObject{
					{
						"type": "shared",
						"rates": []assert.JSONObject{
							{
								"name":   name,
								"limit":  limit,
								"window": window.String(),
							},
						},
					},
				},
			},
		}
	}

	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/rates/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 202,
		Body:         makeRequest(rateName, expectedLimit, expectedWindow),
	}.Check(t, s.Handler)

	getProjectRateQuery := `
		SELECT pra.id, pra.rate_limit, pra.window_ns
		FROM project_rates pra
		JOIN rates ra ON ra.id = pra.rate_id
		JOIN services s ON s.id = ra.service_id
		JOIN projects p ON p.id = pra.project_id
		WHERE p.name = $1 AND s.type = $2 AND ra.name = $3`
	err = s.DB.QueryRow(getProjectRateQuery, "berlin", "shared", rateName).Scan(&projectRateId, &actualLimit, &actualWindow)
	if err != nil {
		t.Fatal(err)
	}
	if actualLimit != expectedLimit {
		t.Errorf(
			"rate limit %s was not updated in database: expected limit %d, but got %d",
			rateName, expectedLimit, actualLimit,
		)
	}
	if actualWindow != expectedWindow {
		t.Errorf(
			"rate limit %s was not updated in database: expected window %d, but got %d",
			rateName, expectedWindow, actualWindow,
		)
	}

	// now we check that an update of the rate limit does not create a new row
	oldProjectRateId := projectRateId
	expectedLimit = uint64(200)
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/rates/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 202,
		Body:         makeRequest(rateName, expectedLimit, expectedWindow),
	}.Check(t, s.Handler)
	err = s.DB.QueryRow(getProjectRateQuery, "berlin", "shared", rateName).Scan(&projectRateId, &actualLimit, &actualWindow)
	if err != nil {
		t.Fatal(err)
	}
	if oldProjectRateId != projectRateId {
		t.Errorf(
			"for rate %s, a new ID was created instead of updating the existing one",
			rateName,
		)
	}
	if actualLimit != expectedLimit {
		t.Errorf(
			"rate limit %s was not updated in database: expected limit %d, but got %d",
			rateName, expectedLimit, actualLimit,
		)
	}
}

func expectStaleProjectServices(t *testing.T, dbm *gorp.DbMap, pairs ...string) {
	t.Helper()

	queryStr := sqlext.SimplifyWhitespace(`
		SELECT p.name, s.type
		 FROM projects p JOIN project_services ps ON ps.project_id = p.id
		 JOIN services s on ps.service_id = s.id
		 WHERE ps.stale
		 ORDER BY p.name, s.type
	`)
	var actualPairs []string

	err := sqlext.ForeachRow(dbm, queryStr, nil, func(rows *sql.Rows) error {
		var (
			projectName string
			serviceType limes.ServiceType
		)
		err := rows.Scan(&projectName, &serviceType)
		if err != nil {
			return err
		}
		actualPairs = append(actualPairs, fmt.Sprintf("%s:%s", projectName, string(serviceType)))
		return nil
	})
	if err != nil {
		t.Fatal(err.Error())
	}

	if !reflect.DeepEqual(pairs, actualPairs) {
		t.Errorf("expected stale project services %v, but got %v", pairs, actualPairs)
	}
}

func Test_EmptyProjectList(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(`{
			"availability_zones": ["az-one", "az-two"],
			"discovery": {
				"method": "static",
				"static_config": {
					"domains": [{"name": "germany", "id": "uuid-for-germany"}],
					"projects": {"uuid-for-germany": []}
				}
			},
			"liquids": {
				"first": {"area": "first"}
			}
		}`),
		test.WithPersistedServiceInfo("first", test.DefaultLiquidServiceInfo()),
		test.WithInitialDiscovery,
		test.WithEmptyRecordsAsNeeded,
	)

	// This warrants its own unit test since the rendering of empty project lists
	// uses a different code path than the rendering of non-empty project lists.
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONObject{"projects": []assert.JSONObject{}},
	}.Check(t, s.Handler)
}

func Test_LargeProjectList(t *testing.T) {
	// to test the behavior of the project list endpoint for large lists,
	// set up a config with a large number of projects (we do it via the discovery config
	// in order to leverage test.WithInitialDiscover and test.WithEmptyRecordsAsNeeded)
	projectUUIDs := make([]liquid.ProjectUUID, 100)
	projectsAsConfigured := make([]core.KeystoneProject, len(projectUUIDs))
	for idx := range projectUUIDs {
		projectUUID := liquid.ProjectUUID(must.Return(uuid.NewV4()).String())
		projectUUIDs[idx] = projectUUID
		projectsAsConfigured[idx] = core.KeystoneProject{
			Name:       fmt.Sprintf("test-project%04d", idx),
			UUID:       projectUUID,
			ParentUUID: "uuid-for-germany",
		}
	}

	configStr := string(must.Return(json.Marshal(core.ClusterConfiguration{
		AvailabilityZones: []limes.AvailabilityZone{"az-one", "az-two"},
		Discovery: core.DiscoveryConfiguration{
			Method: "static",
			StaticDiscoveryConfiguration: core.StaticDiscoveryConfiguration{
				Domains:  []core.KeystoneDomain{{Name: "germany", UUID: "uuid-for-germany"}},
				Projects: map[string][]core.KeystoneProject{"uuid-for-germany": projectsAsConfigured},
			},
		},
		Liquids: map[db.ServiceType]core.LiquidConfiguration{
			"shared":   {Area: "shared"},
			"unshared": {Area: "unshared"},
		},
	})))

	s := test.NewSetup(t,
		test.WithConfig(configStr),
		test.WithPersistedServiceInfo("shared", test.DefaultLiquidServiceInfo()),
		test.WithPersistedServiceInfo("unshared", test.DefaultLiquidServiceInfo()),
		test.WithInitialDiscovery,
		test.WithEmptyRecordsAsNeeded,
	)

	// fill various fields that `test.WithEmptyRecordsAsNeeded` initializes empty with reasonably plausible dummy values
	// (all those queries take an index into the project list as $1 and the project UUID as $2)
	queries := []string{
		`UPDATE project_services SET scraped_at = TO_TIMESTAMP($1) AT LOCAL WHERE project_id = (SELECT id FROM projects WHERE uuid = $2)`,
		fmt.Sprintf(
			`UPDATE project_az_resources SET quota = $1, backend_quota = $1 WHERE project_id = (SELECT id FROM projects WHERE uuid = $2) AND az_resource_id IN (%d, %d)`,
			s.GetAZResourceID("unshared", "things", liquid.AvailabilityZoneAny), s.GetAZResourceID("unshared", "things", liquid.AvailabilityZoneTotal),
		),
		fmt.Sprintf(
			`UPDATE project_az_resources SET usage = $1 / 2 WHERE project_id = (SELECT id FROM projects WHERE uuid = $2) AND az_resource_id IN (%d, %d)`,
			s.GetAZResourceID("unshared", "things", liquid.AvailabilityZoneAny), s.GetAZResourceID("unshared", "things", liquid.AvailabilityZoneTotal),
		),
	}
	for _, query := range queries {
		err := sqlext.WithPreparedStatement(s.DB, query, func(stmt *sql.Stmt) error {
			for idx, uuid := range projectUUIDs {
				_, err := stmt.Exec(idx, uuid)
				if err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// build expectation for what the project list will look like
	expectedProjectsJSON := make([]assert.JSONObject, len(projectUUIDs))
	for idx, projectUUID := range projectUUIDs {
		expectedProjectsJSON[idx] = assert.JSONObject{
			"id":        projectUUID,
			"name":      fmt.Sprintf("test-project%04d", idx),
			"parent_id": "uuid-for-germany",
			"services": []assert.JSONObject{
				{
					"type":       "shared",
					"area":       "shared",
					"scraped_at": idx,
					"resources": []assert.JSONObject{
						{
							"name":                     "capacity",
							"unit":                     "B",
							"quota_distribution_model": "autogrow",
							"quota":                    0,
							"usable_quota":             0,
							"usage":                    0,
						},
						{
							"name":                     "things",
							"quota_distribution_model": "autogrow",
							"quota":                    0,
							"usable_quota":             0,
							"usage":                    0,
						},
					},
				},
				{
					"type":       "unshared",
					"area":       "unshared",
					"scraped_at": idx,
					"resources": []assert.JSONObject{
						{
							"name":                     "capacity",
							"unit":                     "B",
							"quota_distribution_model": "autogrow",
							"quota":                    0,
							"usable_quota":             0,
							"usage":                    0,
						},
						{
							"name":                     "things",
							"quota_distribution_model": "autogrow",
							"quota":                    idx,
							"usable_quota":             idx,
							"usage":                    idx / 2,
						},
					},
				},
			},
		}
	}
	sort.Slice(expectedProjectsJSON, func(i, j int) bool {
		left := expectedProjectsJSON[i]
		right := expectedProjectsJSON[j]
		return left["id"].(liquid.ProjectUUID) < right["id"].(liquid.ProjectUUID)
	})

	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONObject{"projects": expectedProjectsJSON},
	}.Check(t, s.Handler)
}

func Test_PutMaxQuotaOnProject(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(`{
			"availability_zones": ["az-one", "az-two"],
			"discovery": {
				"method": "static",
				"static_config": {
					"domains": [{"name": "germany", "id": "uuid-for-germany"}],
					"projects": {
						"uuid-for-germany": [{"name": "berlin", "id": "uuid-for-berlin", "parent_id": "uuid-for-germany"}]
					}
				}
			},
			"liquids": {
				"shared": {"area": "shared"}
			}
		}`),
		test.WithPersistedServiceInfo("shared", test.DefaultLiquidServiceInfo()),
		test.WithInitialDiscovery,
		test.WithEmptyRecordsAsNeeded,
	)

	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.Ignore()

	makeRequest := func(serviceType limes.ServiceType, resources ...any) assert.JSONObject {
		return assert.JSONObject{
			"project": assert.JSONObject{
				"services": []assert.JSONObject{{
					"type":      serviceType,
					"resources": resources,
				}},
			},
		}
	}

	// happy case: set a non-null value for the first time, then update it
	for _, value := range []uint64{500, 1000} {
		assert.HTTPRequest{
			Method:       "PUT",
			Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/max-quota",
			Body:         makeRequest("shared", assert.JSONObject{"name": "things", "max_quota": value}),
			ExpectStatus: http.StatusAccepted,
		}.Check(t, s.Handler)
		tr.DBChanges().AssertEqualf(`
			UPDATE project_resources SET max_quota_from_outside_admin = %d WHERE id = 2 AND project_id = 1 AND resource_id = 2;
		`, value)
	}

	// happy case: write a NULL value over both an existing NULL value and a non-NULL value
	assert.HTTPRequest{
		Method: "PUT",
		Path:   "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/max-quota",
		Body: makeRequest("shared",
			assert.JSONObject{"name": "things", "max_quota": nil},
			assert.JSONObject{"name": "capacity", "max_quota": nil},
		),
		ExpectStatus: http.StatusAccepted,
	}.Check(t, s.Handler)
	tr.DBChanges().AssertEqualf(`
		UPDATE project_resources SET max_quota_from_outside_admin = NULL WHERE id = 2 AND project_id = 1 AND resource_id = 2;
	`)

	// happy case: set value with unit conversion
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/max-quota",
		Body:         makeRequest("shared", assert.JSONObject{"name": "capacity", "max_quota": 10, "unit": "KiB"}),
		ExpectStatus: http.StatusAccepted,
	}.Check(t, s.Handler)
	tr.DBChanges().AssertEqualf(`
		UPDATE project_resources SET max_quota_from_outside_admin = 10240 WHERE id = 1 AND project_id = 1 AND resource_id = 1;
	`)

	s.TokenValidator.Enforcer.AllowEditMaxQuota = false
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/max-quota",
		Body:         makeRequest("shared", assert.JSONObject{"name": "things", "max_quota": 500}),
		ExpectStatus: http.StatusForbidden,
	}.Check(t, s.Handler)
	s.TokenValidator.Enforcer.AllowEditMaxQuota = true

	// error case: invalid service
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/max-quota",
		Body:         makeRequest("unknown", assert.JSONObject{"name": "things", "max_quota": 1000}),
		ExpectStatus: http.StatusUnprocessableEntity,
		ExpectBody:   assert.StringData("no such service and/or resource: unknown/things\n"),
	}.Check(t, s.Handler)

	// error case: invalid resource
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/max-quota",
		Body:         makeRequest("shared", assert.JSONObject{"name": "items", "max_quota": 1000}),
		ExpectStatus: http.StatusUnprocessableEntity,
		ExpectBody:   assert.StringData("no such service and/or resource: shared/items\n"),
	}.Check(t, s.Handler)

	// error case: resource does not track quota
	s.MustDBExec("UPDATE resources SET has_quota = FALSE WHERE path = $1", "shared/capacity")
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/max-quota",
		Body:         makeRequest("shared", assert.JSONObject{"name": "capacity", "max_quota": 1000}),
		ExpectStatus: http.StatusUnprocessableEntity,
		ExpectBody:   assert.StringData("resource shared/capacity does not track quota\n"),
	}.Check(t, s.Handler)

	// error case: invalid unit
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/max-quota",
		Body:         makeRequest("shared", assert.JSONObject{"name": "things", "max_quota": 1000, "unit": "MiB"}),
		ExpectStatus: http.StatusUnprocessableEntity,
		ExpectBody:   assert.StringData("invalid input for shared/things: cannot convert value from MiB to <count> because units are incompatible\n"),
	}.Check(t, s.Handler)
}

func Test_PutQuotaAutogrowth(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(`{
			"availability_zones": ["az-one", "az-two"],
			"discovery": {
				"method": "static",
				"static_config": {
					"domains": [{"name": "germany", "id": "uuid-for-germany"}],
					"projects": {
						"uuid-for-germany": [{"name": "berlin", "id": "uuid-for-berlin", "parent_id": "uuid-for-germany"}]
					}
				}
			},
			"liquids": {
				"shared": {
					"area": "shared",
					"commitment_behavior_per_resource": [
						{
							"key": "capacity|things",
							"value": {
								"durations_per_domain": [{"key": ".+", "value": ["1 hour", "2 hours"]}]
							}
						}
					]
				},
				"unshared": {"area": "unshared"}
			}
		}`),
		test.WithPersistedServiceInfo("shared", test.DefaultLiquidServiceInfo()),
		test.WithPersistedServiceInfo("unshared", test.DefaultLiquidServiceInfo()),
		test.WithInitialDiscovery,
		test.WithEmptyRecordsAsNeeded,
	)

	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.Ignore()

	makeRequest := func(serviceType limes.ServiceType, resources ...any) assert.JSONObject {
		return assert.JSONObject{
			"project": assert.JSONObject{
				"services": []assert.JSONObject{{
					"type":      serviceType,
					"resources": resources,
				}},
			},
		}
	}

	// happy case: enable autogrowth twice, only update the database once.
	for range 2 {
		assert.HTTPRequest{
			Method:       "PUT",
			Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/forbid-autogrowth",
			Body:         makeRequest("shared", assert.JSONObject{"name": "things", "forbid_autogrowth": true}),
			ExpectStatus: http.StatusAccepted,
		}.Check(t, s.Handler)
	}
	tr.DBChanges().AssertEqualf(`UPDATE project_resources SET forbid_autogrowth = TRUE WHERE id = 2 AND project_id = 1 AND resource_id = 2;`)

	// happy case: disable autogrowth
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/forbid-autogrowth",
		Body:         makeRequest("shared", assert.JSONObject{"name": "things", "forbid_autogrowth": false}),
		ExpectStatus: http.StatusAccepted,
	}.Check(t, s.Handler)
	tr.DBChanges().AssertEqualf(`UPDATE project_resources SET forbid_autogrowth = FALSE WHERE id = 2 AND project_id = 1 AND resource_id = 2;`)

	// happy case: multiple resources.
	assert.HTTPRequest{
		Method: "PUT",
		Path:   "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/forbid-autogrowth",
		Body: makeRequest("shared",
			assert.JSONObject{"name": "things", "forbid_autogrowth": true},
			assert.JSONObject{"name": "capacity", "forbid_autogrowth": true},
		),
		ExpectStatus: http.StatusAccepted,
	}.Check(t, s.Handler)
	tr.DBChanges().AssertEqualf(`
		UPDATE project_resources SET forbid_autogrowth = TRUE WHERE id = 1 AND project_id = 1 AND resource_id = 1;
		UPDATE project_resources SET forbid_autogrowth = TRUE WHERE id = 2 AND project_id = 1 AND resource_id = 2;
	`)

	// error case: missing the appropriate edit permission
	s.TokenValidator.Enforcer.AllowEdit = false
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/forbid-autogrowth",
		Body:         makeRequest("shared", assert.JSONObject{"name": "things", "forbid_autogrowth": true}),
		ExpectStatus: http.StatusForbidden,
		ExpectBody:   assert.StringData("Forbidden\n"),
	}.Check(t, s.Handler)
	s.TokenValidator.Enforcer.AllowEdit = true

	// error case: malformed request
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/forbid-autogrowth",
		Body:         makeRequest("shared", assert.JSONObject{"name": "things", "forbid_auto": true}),
		ExpectStatus: http.StatusUnprocessableEntity,
		ExpectBody:   assert.StringData("malformed request body for resource: shared/things\n"),
	}.Check(t, s.Handler)

	// error case: invalid service
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/forbid-autogrowth",
		Body:         makeRequest("unknown", assert.JSONObject{"name": "things", "forbid_autogrowth": true}),
		ExpectStatus: http.StatusUnprocessableEntity,
		ExpectBody:   assert.StringData("no such service and/or resource: unknown/things\n"),
	}.Check(t, s.Handler)

	// error case: invalid resource
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/forbid-autogrowth",
		Body:         makeRequest("shared", assert.JSONObject{"name": "items", "forbid_autogrowth": true}),
		ExpectStatus: http.StatusUnprocessableEntity,
		ExpectBody:   assert.StringData("no such service and/or resource: shared/items\n"),
	}.Check(t, s.Handler)

	// error case: resource does not allow commitments (we only allow setting
	// forbid_autogrowth on resources that track commitments because, on other
	// resources, this will usually lead to the user locking themselves out of
	// using the resource entirely)
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/forbid-autogrowth",
		Body:         makeRequest("unshared", assert.JSONObject{"name": "capacity", "forbid_autogrowth": true}),
		ExpectStatus: http.StatusUnprocessableEntity,
		ExpectBody:   assert.StringData("resource unshared/capacity does not allow commitments\n"),
	}.Check(t, s.Handler)

	// error case: resource does not track quota
	s.MustDBExec("UPDATE resources SET has_quota = FALSE WHERE path = $1", "shared/capacity")

	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/forbid-autogrowth",
		Body:         makeRequest("shared", assert.JSONObject{"name": "capacity", "forbid_autogrowth": true}),
		ExpectStatus: http.StatusUnprocessableEntity,
		ExpectBody:   assert.StringData("resource shared/capacity does not track quota\n"),
	}.Check(t, s.Handler)
}

func Test_Historical_Usage(t *testing.T) {
	s := setupTest(t)

	query := `UPDATE project_az_resources SET usage = $1, historical_usage = $2 WHERE project_id = $3 AND az_resource_id = $4`
	s.MustDBExec(query, 2, `{"t":[1719399600, 1719486000],"v":[1, 5]}`, s.GetProjectID("berlin"), s.GetAZResourceID("shared", "capacity", "az-one"))
	s.MustDBExec(query, 3, `{"t":[1719399600, 1719486000],"v":[2, 6]}`, s.GetProjectID("berlin"), s.GetAZResourceID("shared", "capacity", liquid.AvailabilityZoneTotal))

	assert.HTTPRequest{
		Method:       "GET",
		Header:       map[string]string{"X-Limes-V2-API-Preview": "per-az"},
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-get-berlin-v2-api.json"),
	}.Check(t, s.Handler)
}

func TestResourceRenaming(t *testing.T) {
	// I want to test with various renaming configs, but matching on the full
	// report is extremely tedious because the types and names are scattered
	// throughout, making a compact match difficult; as a proxy, we set a different
	// commitment duration on each resource and then use those values to identify
	// the resources post renaming
	s := test.NewSetup(t,
		test.WithConfig(`{
			"availability_zones": ["az-one", "az-two"],
			"discovery": {
				"method": "static",
				"static_config": {
					"domains": [{"name": "germany", "id": "uuid-for-germany"}],
					"projects": {
						"uuid-for-germany": [{"name": "berlin", "id": "uuid-for-berlin", "parent_id": "uuid-for-germany"}]
					}
				}
			},
			"liquids": {
				"shared": {
					"area": "shared",
					"commitment_behavior_per_resource": [
						{"key": "capacity", "value": {"durations_per_domain": [{"key": ".*", "value": ["2 seconds"]}]}},
						{"key": "things", "value": {"durations_per_domain": [{"key": ".*", "value": ["3 seconds"]}]}}
					]
				},
				"unshared": {
					"area": "unshared",
					"commitment_behavior_per_resource": [
						{"key": "capacity", "value": {"durations_per_domain": [{"key": ".*", "value": ["4 seconds"]}]}},
						{"key": "things", "value": {"durations_per_domain": [{"key": ".*", "value": ["5 seconds"]}]}}
					]
				}
			}
		}`),
		test.WithPersistedServiceInfo("shared", test.DefaultLiquidServiceInfo()),
		test.WithPersistedServiceInfo("unshared", test.DefaultLiquidServiceInfo()),
		test.WithInitialDiscovery,
		test.WithEmptyRecordsAsNeeded,
	)

	// helper function that makes one GET query per structural level and checks
	// that commitment durations appear on the right resources in the right
	// services
	//
	// (there is an unfortunate amount of duplication between the
	// project/domain/cluster level checks here because the different report
	// types make it difficult to write this generically)
	expect := func(query string, expectedDurations ...string) {
		t.Helper()

		////// project level

		var projectData struct {
			Report limesresources.ProjectReport `json:"project"`
		}
		assert.HTTPRequest{
			Method:       "GET",
			Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin" + query,
			ExpectStatus: 200,
			ExpectBody:   JSONThatUnmarshalsInto{Value: &projectData},
		}.Check(t, s.Handler)

		var actualDurationsInProject []string
		for serviceType, serviceReport := range projectData.Report.Services {
			for resourceName, resourceReport := range serviceReport.Resources {
				if resourceReport.CommitmentConfig != nil {
					for _, duration := range resourceReport.CommitmentConfig.Durations {
						msg := fmt.Sprintf("%s: %s/%s", duration.String(), serviceType, resourceName)
						actualDurationsInProject = append(actualDurationsInProject, msg)
					}
				}
			}
		}
		sort.Strings(actualDurationsInProject)
		assert.DeepEqual(t, "durations on project level with query "+query, actualDurationsInProject, expectedDurations)

		////// domain level

		var domainData struct {
			Report limesresources.DomainReport `json:"domain"`
		}
		assert.HTTPRequest{
			Method:       "GET",
			Path:         "/v1/domains/uuid-for-germany" + query,
			ExpectStatus: 200,
			ExpectBody:   JSONThatUnmarshalsInto{Value: &domainData},
		}.Check(t, s.Handler)

		var actualDurationsInDomain []string
		for serviceType, serviceReport := range domainData.Report.Services {
			for resourceName, resourceReport := range serviceReport.Resources {
				if resourceReport.CommitmentConfig != nil {
					for _, duration := range resourceReport.CommitmentConfig.Durations {
						msg := fmt.Sprintf("%s: %s/%s", duration.String(), serviceType, resourceName)
						actualDurationsInDomain = append(actualDurationsInDomain, msg)
					}
				}
			}
		}
		sort.Strings(actualDurationsInDomain)
		assert.DeepEqual(t, "durations on domain level with query "+query, actualDurationsInDomain, expectedDurations)

		////// cluster level

		var clusterData struct {
			Report limesresources.ClusterReport `json:"cluster"`
		}
		assert.HTTPRequest{
			Method:       "GET",
			Path:         "/v1/clusters/current" + query,
			ExpectStatus: 200,
			ExpectBody:   JSONThatUnmarshalsInto{Value: &clusterData},
		}.Check(t, s.Handler)

		var actualDurationsInCluster []string
		for serviceType, serviceReport := range clusterData.Report.Services {
			for resourceName, resourceReport := range serviceReport.Resources {
				if resourceReport.CommitmentConfig != nil {
					for _, duration := range resourceReport.CommitmentConfig.Durations {
						msg := fmt.Sprintf("%s: %s/%s", duration.String(), serviceType, resourceName)
						actualDurationsInCluster = append(actualDurationsInCluster, msg)
					}
				}
			}
		}
		sort.Strings(actualDurationsInCluster)
		assert.DeepEqual(t, "durations on cluster level with query "+query, actualDurationsInCluster, expectedDurations)
	}

	// baseline
	s.Cluster.Config.ResourceBehaviors = nil
	expect("?",
		"2 seconds: shared/capacity",
		"3 seconds: shared/things",
		"4 seconds: unshared/capacity",
		"5 seconds: unshared/things",
	)
	expect("?service=shared",
		"2 seconds: shared/capacity",
		"3 seconds: shared/things",
	)
	expect("?resource=things",
		"3 seconds: shared/things",
		"5 seconds: unshared/things",
	)

	// rename resources within a service
	s.Cluster.Config.ResourceBehaviors = []core.ResourceBehavior{{
		FullResourceNameRx: "shared/things",
		IdentityInV1API:    core.ResourceRef{ServiceType: "shared", Name: "items"},
	}}
	expect("?",
		"2 seconds: shared/capacity",
		"3 seconds: shared/items",
		"4 seconds: unshared/capacity",
		"5 seconds: unshared/things",
	)
	expect("?service=shared",
		"2 seconds: shared/capacity",
		"3 seconds: shared/items",
	)
	expect("?resource=items",
		"3 seconds: shared/items",
	)
	expect("?resource=things",
		"5 seconds: unshared/things",
	)

	// move resource to a different, existing service
	s.Cluster.Config.ResourceBehaviors = []core.ResourceBehavior{{
		FullResourceNameRx: "shared/things",
		IdentityInV1API:    core.ResourceRef{ServiceType: "unshared", Name: "other_things"},
	}}
	expect("?",
		"2 seconds: shared/capacity",
		"3 seconds: unshared/other_things",
		"4 seconds: unshared/capacity",
		"5 seconds: unshared/things",
	)
	expect("?service=shared",
		"2 seconds: shared/capacity",
	)
	expect("?service=unshared",
		"3 seconds: unshared/other_things",
		"4 seconds: unshared/capacity",
		"5 seconds: unshared/things",
	)
	expect("?resource=other_things",
		"3 seconds: unshared/other_things",
	)
	expect("?resource=things",
		"5 seconds: unshared/things",
	)

	// move resource to a different, new service
	s.Cluster.Config.ResourceBehaviors = []core.ResourceBehavior{
		{
			FullResourceNameRx: "shared/capacity",
			IdentityInV1API:    core.ResourceRef{ServiceType: "shared_capacity", Name: "all"},
		},
	}
	expect("?",
		"2 seconds: shared_capacity/all",
		"3 seconds: shared/things",
		"4 seconds: unshared/capacity",
		"5 seconds: unshared/things",
	)
	expect("?service=shared",
		"3 seconds: shared/things",
	)
	expect("?service=shared_capacity",
		"2 seconds: shared_capacity/all",
	)
	expect("?resource=all",
		"2 seconds: shared_capacity/all",
	)
	expect("?resource=capacity",
		"4 seconds: unshared/capacity",
	)
}

// JSONThatUnmarshalsInto is an implementor of the assert.HTTPResponseBody interface that
// checks that the response body unmarshals cleanly into the given value. The wrapped
// value must be of a pointer type.
//
// This can be used instead of assert.JSONObject if the test wants to capture
// the response in a structured form to perform further computations and/or
// assertions afterwards.
//
// TODO: upstream this into go-bits if we like it
type JSONThatUnmarshalsInto struct {
	Value any
}

// AssertResponseBody implements the HTTPResponseBody interface.
func (j JSONThatUnmarshalsInto) AssertResponseBody(t *testing.T, requestInfo string, responseBody []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(responseBody))
	dec.DisallowUnknownFields()
	err := dec.Decode(j.Value)
	if err != nil {
		t.Errorf("%s: could not decode response as %T", requestInfo, j.Value)
		t.Logf("%s: response body was %q", requestInfo, responseBody)
		return false
	}
	return true
}

const testAZSeparatedConfigJSON = `{
	"availability_zones": ["az-one", "az-two"],
	"discovery": {
		"method": "static",
		"static_config": {
			"domains": [{"name": "germany", "id": "uuid-for-germany"}],
			"projects": {
				"uuid-for-germany": [{"name": "berlin", "id": "uuid-for-berlin", "parent_id": "uuid-for-germany"}]
			}
		}
	},
	"liquids": {
		"shared": {
			"area": "shared"
		}
	},
	"resource_behavior": [
		{
			// check that category mapping is reported
			"resource": ".+/capacity_az_separated",
			"category": "foo_category"
		}
	]
}`

func Test_SeparatedTopologyOperations(t *testing.T) {
	srvInfo := liquid.ServiceInfo{
		Version: 1,
		Resources: map[liquid.ResourceName]liquid.ResourceInfo{
			"capacity_az_separated": {
				Unit:     liquid.UnitBytes,
				Topology: liquid.AZSeparatedTopology,
				HasQuota: true,
			},
		},
	}
	s := test.NewSetup(t,
		test.WithConfig(testAZSeparatedConfigJSON),
		test.WithPersistedServiceInfo("shared", srvInfo),
		test.WithInitialDiscovery,
		test.WithEmptyRecordsAsNeeded,
	)

	s.MustDBExec(`
		UPDATE project_services SET scraped_at = $1, checked_at = $1
	`, time.Unix(22, 0))
	s.MustDBExec(`
		UPDATE project_az_resources SET backend_quota = 5, quota = 5, usage = 1 WHERE az_resource_id IN (
			SELECT id FROM az_resources WHERE az = $1 OR az = $2
		)
	`, "az-one", "az-two")
	s.MustDBExec(`
		UPDATE project_az_resources SET backend_quota = 10, quota = 10, usage = 2 WHERE az_resource_id IN (
			SELECT id FROM az_resources WHERE az = $1
		)
	`, liquid.AvailabilityZoneTotal)

	// This test ensures that the consumable limes APIs do not break with the introduction (or further changes) of the az separated topology.
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/clusters/current",
		Header:       map[string]string{"X-Limes-V2-API-Preview": "per-az"},
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("fixtures/cluster-get-az-separated.json"),
	}.Check(t, s.Handler)

	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains",
		Header:       map[string]string{"X-Limes-V2-API-Preview": "per-az"},
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/domain-list-az-separated.json"),
	}.Check(t, s.Handler)

	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects",
		Header:       map[string]string{"X-Limes-V2-API-Preview": "per-az"},
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-list-az-separated.json"),
	}.Check(t, s.Handler)
}
