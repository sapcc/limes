// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
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
	"github.com/sapcc/go-bits/regexpext"
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
	testConfigYAML = `
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
				rate_limits:
					global:
						- name:   service/shared/objects:create
							limit:  5000
							window: 1s
					project_default:
						- name:   service/shared/objects:create
							limit:  5
							window: 1m
						- name:   service/shared/objects:delete
							limit:  1
							window: 1m
						- name:   service/shared/objects:update
							limit:  2
							window: 1s
						- name:   service/shared/objects:read/list
							limit:  3
							window: 1s
				commitment_behavior_per_resource:
					- key: 'capacity|things'
						value:
							durations_per_domain: [{ key: '.+', value: ["1 hour", "2 hours"] }]
							min_confirm_date: '1970-01-08T00:00:00Z' # one week after start of mock.Clock

			unshared:
				area: unshared
				liquid_service_type: %[2]s
				rate_limits:
					project_default:
						- name:   service/unshared/instances:create
							limit:  5
							window: 1m
						- name:   service/unshared/instances:delete
							limit:  1
							window: 1m
						- name:   service/unshared/instances:update
							limit:  2
							window: 1s

		resource_behavior:
			# check that category mapping is reported
			- resource: '.+/capacity_az_separated'
				category: foo_category
	`
)

func setupTest(t *testing.T, startData string) (s test.Setup) {
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
	_, liquidServiceTypeShared := test.NewMockLiquidClient(srvInfoShared)
	_, liquidServiceTypeUnshared := test.NewMockLiquidClient(srvInfoUnshared)

	t.Helper()
	s = test.NewSetup(t,
		test.WithDBFixtureFile(startData),
		test.WithConfig(fmt.Sprintf(testConfigYAML, liquidServiceTypeShared, liquidServiceTypeUnshared)),
		test.WithAPIHandler(NewV1API),
	)
	return
}

func Test_ScrapeErrorOperations(t *testing.T) {
	s := setupTest(t, "fixtures/start-data.sql")

	// Add a scrape error to one specific service with type 'unshared'.
	_, err := s.DB.Exec(`UPDATE project_services ps SET scrape_error_message = $1
		FROM services cs WHERE ps.service_id = cs.id AND ps.id = $2 AND cs.type = $3`,
		"could not scrape this specific unshared service",
		1, "unshared",
	)
	if err != nil {
		t.Fatal(err)
	}

	// Add the same scrape error to all services with type 'shared'. This will ensure that
	// they get grouped under a dummy project.
	_, err = s.DB.Exec(`UPDATE project_services ps SET scrape_error_message = $1
		FROM services cs WHERE ps.service_id = cs.id AND cs.type = $2`,
		"could not scrape shared service",
		"shared",
	)
	if err != nil {
		t.Fatal(err)
	}

	// check ListScrapeErrors
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/admin/scrape-errors",
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/scrape-error-list.json"),
	}.Check(t, s.Handler)
}

func Test_EmptyScrapeErrorReport(t *testing.T) {
	s := setupTest(t, "/dev/null")

	// check ListScrapeErrors
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/admin/scrape-errors",
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/scrape-error-empty.json"),
	}.Check(t, s.Handler)
}

func Test_RateScrapeErrorOperations(t *testing.T) {
	s := setupTest(t, "fixtures/start-data.sql")

	// Add a scrape error to one specific service with type 'unshared' that has rate data.
	_, err := s.DB.Exec(`UPDATE project_services ps SET scrape_error_message = $1
		FROM services cs WHERE ps.service_id = cs.id AND ps.id = $2 AND cs.type = $3`,
		"could not scrape rate data for this specific unshared service",
		1, "unshared",
	)
	if err != nil {
		t.Fatal(err)
	}

	// Add the same scrape error to both services with type 'shared' that have rate data.
	// This will ensure that they get grouped under a dummy project.
	_, err = s.DB.Exec(`UPDATE project_services ps SET scrape_error_message = $1
		FROM services cs WHERE ps.service_id = cs.id AND (ps.id = $2 OR ps.id = $3) AND type = $4`,
		"could not scrape rate data for shared service",
		2, 4, "shared",
	)
	if err != nil {
		t.Fatal(err)
	}

	// check ListRateScrapeErrors
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/admin/scrape-errors",
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/rate-scrape-error-list.json"),
	}.Check(t, s.Handler)
}

func Test_EmptyRateScrapeErrorReport(t *testing.T) {
	s := setupTest(t, "/dev/null")

	// check ListRateScrapeErrors
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/admin/scrape-errors",
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/rate-scrape-error-empty.json"),
	}.Check(t, s.Handler)
}

func Test_ClusterOperations(t *testing.T) {
	s := setupTest(t, "fixtures/start-data.sql")

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
	s := setupTest(t, "fixtures/start-data.sql")
	discovery := s.Cluster.DiscoveryPlugin.(*core.StaticDiscoveryPlugin)

	// all reports are pulled at the same simulated time, `s.Clock().Now().Unix() == 3600`,
	// to match the setup of active vs. expired commitments in `fixtures/start-data.sql`
	s.Clock.StepBy(1 * time.Hour)

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
	s := setupTest(t, "fixtures/start-data.sql")
	discovery := s.Cluster.DiscoveryPlugin.(*core.StaticDiscoveryPlugin)

	// all reports are pulled at the same simulated time, `s.Clock().Now().Unix() == 3600`,
	// to match the setup of active vs. expired commitments in `fixtures/start-data.sql`
	s.Clock.StepBy(1 * time.Hour)

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

	// paris returns lowest max_quota setting
	_, dberr := s.DB.Exec("UPDATE project_resources SET max_quota_from_outside_admin=300, max_quota_from_local_admin=200 where id=12")
	if dberr != nil {
		t.Fatal(dberr)
	}
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-france/projects/uuid-for-paris",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-get-paris.json"),
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
	_, err := s.DB.Exec(`UPDATE project_services SET stale = FALSE WHERE project_id = (SELECT id FROM projects WHERE name = $1)`, "frankfurt")
	if err != nil {
		t.Fatal(err)
	}

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
	err = s.DB.QueryRow(`
		SELECT pra.rate_limit, pra.window_ns
		FROM project_rates pra
		JOIN rates cra ON cra.id = pra.rate_id
		JOIN services cs ON cs.id = cra.service_id
		JOIN projects p ON p.id = pra.project_id
		WHERE p.name = $1 AND cs.type = $2 AND cra.name = $3`,
		"berlin", "shared", "service/shared/notexistent:bogus").Scan(&actualLimit, &actualWindow)
	// There shouldn't be anything in the DB.
	if err.Error() != "sql: no rows in result set" {
		t.Fatalf("expected error %v but got %v", "sql: no rows in result set", err)
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
		JOIN rates cra ON cra.id = pra.rate_id
		JOIN services cs ON cs.id = cra.service_id
		JOIN projects p ON p.id = pra.project_id
		WHERE p.name = $1 AND cs.type = $2 AND cra.name = $3`
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
		SELECT p.name, cs.type
		 FROM projects p JOIN project_services ps ON ps.project_id = p.id
		 JOIN services cs on ps.service_id = cs.id
		 WHERE ps.stale
		 ORDER BY p.name, cs.type
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
	s := setupTest(t, "fixtures/start-data.sql")

	_, err := s.DB.Exec(`DELETE FROM project_commitments`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(`DELETE FROM projects`)
	if err != nil {
		t.Fatal(err)
	}

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
	// start without any projects pre-defined in the start data
	s := setupTest(t, "fixtures/start-data-minimal.sql")
	// we don't care about the various ResourceBehaviors in this test
	s.Cluster.Config.ResourceBehaviors = nil
	for idx, scfg := range s.Cluster.Config.Liquids {
		scfg.CommitmentBehaviorPerResource = nil
		s.Cluster.Config.Liquids[idx] = scfg
	}

	// template for how a single project will look in the output JSON
	makeProjectJSON := func(idx int, projectName string, projectUUID liquid.ProjectUUID) assert.JSONObject {
		return assert.JSONObject{
			"id":        projectUUID,
			"name":      projectName,
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
	var expectedProjectsJSON []assert.JSONObject

	serviceIDByType := map[db.ServiceType]db.ServiceID{
		"unshared": 1,
		"shared":   2,
	}
	resourceIDByNameByType := map[db.ServiceType]map[liquid.ResourceName]db.ResourceID{
		"unshared": {
			"things":   1,
			"capacity": 2,
		},
		"shared": {
			"things":   3,
			"capacity": 4,
		},
	}

	// set up a large number of projects to test the behavior of the project list endpoint for large lists
	projectCount := 100
	for idx := 1; idx <= projectCount; idx++ {
		projectUUIDGen, err := uuid.NewV4()
		if err != nil {
			t.Fatal(err)
		}
		projectName := fmt.Sprintf("test-project%04d", idx)
		projectUUID := liquid.ProjectUUID(projectUUIDGen.String())
		scrapedAt := time.Unix(int64(idx), 0).UTC()
		expectedProjectsJSON = append(expectedProjectsJSON, makeProjectJSON(idx, projectName, projectUUID))

		project := db.Project{
			DomainID:   1,
			ParentUUID: "uuid-for-germany",
			Name:       projectName,
			UUID:       projectUUID,
		}
		err = s.DB.Insert(&project)
		if err != nil {
			t.Fatal(err)
		}
		for _, serviceType := range []db.ServiceType{"shared", "unshared"} {
			service := db.ProjectService{
				ProjectID: project.ID,
				ServiceID: serviceIDByType[serviceType],
				ScrapedAt: Some(scrapedAt),
				CheckedAt: Some(scrapedAt),
			}
			err = s.DB.Insert(&service)
			if err != nil {
				t.Fatal(err)
			}
			for _, resourceName := range []liquid.ResourceName{"things", "capacity"} {
				resource := db.ProjectResource{
					ProjectID:    project.ID,
					ResourceID:   resourceIDByNameByType[serviceType][resourceName],
					Quota:        Some[uint64](0),
					BackendQuota: Some[int64](0),
				}
				azResource := db.ProjectAZResource{
					ProjectID: project.ID,
					//
					AZResourceID: db.AZResourceID(resourceIDByNameByType[serviceType][resourceName]),
					Usage:        0,
				}
				if serviceType == "unshared" && resourceName == "things" {
					resource.Quota = Some(uint64(idx))
					azResource.Usage = uint64(idx / 2) //nolint:gosec // idx is hardcoded in test
					resource.BackendQuota = Some(int64(idx))
				}
				err = s.DB.Insert(&resource)
				if err != nil {
					t.Fatal(err)
				}
				err = s.DB.Insert(&azResource)
				if err != nil {
					t.Fatal(err)
				}
			}
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
	s := setupTest(t, "fixtures/start-data.sql")

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
			UPDATE project_resources SET max_quota_from_outside_admin = %d WHERE id = 3 AND project_id = 1 AND resource_id = 3;
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
		UPDATE project_resources SET max_quota_from_outside_admin = NULL WHERE id = 3 AND project_id = 1 AND resource_id = 3;
	`)

	// happy case: set value with unit conversion
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/max-quota",
		Body:         makeRequest("shared", assert.JSONObject{"name": "capacity", "max_quota": 10, "unit": "KiB"}),
		ExpectStatus: http.StatusAccepted,
	}.Check(t, s.Handler)
	tr.DBChanges().AssertEqualf(`
		UPDATE project_resources SET max_quota_from_outside_admin = 10240 WHERE id = 4 AND project_id = 1 AND resource_id = 4;
	`)

	// happy case: set max quota with project permissions
	s.TokenValidator.Enforcer.AllowEditMaxQuota = false
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/max-quota",
		Body:         makeRequest("shared", assert.JSONObject{"name": "things", "max_quota": 500}),
		ExpectStatus: http.StatusAccepted,
	}.Check(t, s.Handler)
	tr.DBChanges().AssertEqualf(`
		UPDATE project_resources SET max_quota_from_local_admin = %d WHERE id = 3 AND project_id = 1 AND resource_id = 3;
	`, 500)
	s.TokenValidator.Enforcer.AllowEditMaxQuota = true

	// error case: missing the appropriate edit permission
	s.TokenValidator.Enforcer.AllowEdit = false
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/max-quota",
		Body:         makeRequest("shared", assert.JSONObject{"name": "things", "max_quota": 1000}),
		ExpectStatus: http.StatusForbidden,
		ExpectBody:   assert.StringData("Forbidden\n"),
	}.Check(t, s.Handler)
	s.TokenValidator.Enforcer.AllowEdit = true

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
	_, err := s.DB.Exec("UPDATE resources SET has_quota = FALSE WHERE id = 4")
	if err != nil {
		t.Error(err)
	}
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

func Test_Historical_Usage(t *testing.T) {
	s := setupTest(t, "fixtures/start-data.sql")
	_, err := s.DB.Exec(`UPDATE project_az_resources SET usage=2, historical_usage='{"t":[1719399600, 1719486000],"v":[1, 5]}' WHERE id=7`)
	if err != nil {
		t.Fatal(err)
	}

	assert.HTTPRequest{
		Method:       "GET",
		Header:       map[string]string{"X-Limes-V2-API-Preview": "per-az"},
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-get-berlin-v2-api.json"),
	}.Check(t, s.Handler)
}

func TestResourceRenaming(t *testing.T) {
	s := setupTest(t, "fixtures/start-data.sql")

	// a shorthand constructor (unfortunately it is hard to construct regexpext.ConfigSet
	// by hand because the element type (the Key/Value pair) not a named type)
	makeDurations := func(d time.Duration) regexpext.ConfigSet[string, []limesresources.CommitmentDuration] {
		result := make(regexpext.ConfigSet[string, []limesresources.CommitmentDuration], 1)
		result[0].Key = ".*"
		result[0].Value = []limesresources.CommitmentDuration{{Short: d}}
		return result
	}

	// I want to test with various renaming configs, but matching on the full
	// report is extremely tedious because the types and names are scattered
	// throughout, making a compact match; as a proxy, we set a different
	// commitment duration on each resource and then use those values to identify
	// the resources post renaming
	for serviceType, l := range s.Cluster.Config.Liquids {
		switch serviceType {
		case "shared":
			l.CommitmentBehaviorPerResource = make(regexpext.ConfigSet[liquid.ResourceName, core.CommitmentBehavior], 3)
			l.CommitmentBehaviorPerResource[0].Key = "capacity"
			l.CommitmentBehaviorPerResource[0].Value.DurationsPerDomain = makeDurations(2 * time.Second)
			l.CommitmentBehaviorPerResource[1].Key = "things"
			l.CommitmentBehaviorPerResource[1].Value.DurationsPerDomain = makeDurations(3 * time.Second)
		case "unshared":
			l.CommitmentBehaviorPerResource = make(regexpext.ConfigSet[liquid.ResourceName, core.CommitmentBehavior], 3)
			l.CommitmentBehaviorPerResource[0].Key = "capacity"
			l.CommitmentBehaviorPerResource[0].Value.DurationsPerDomain = makeDurations(4 * time.Second)
			l.CommitmentBehaviorPerResource[1].Key = "things"
			l.CommitmentBehaviorPerResource[1].Value.DurationsPerDomain = makeDurations(5 * time.Second)
		}
		s.Cluster.Config.Liquids[serviceType] = l
	}

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

func Test_SeparatedTopologyOperations(t *testing.T) {
	// This test structure ensures that the consumable limes APIs do not break with the introduction (or further changes) of the az separated topology.
	s := setupTest(t, "fixtures/start-data-az-separated.sql")
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
