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

package api

import (
	"database/sql"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	policy "github.com/databus23/goslo.policy"
	"github.com/go-gorp/gorp/v3"
	"github.com/gofrs/uuid"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/test"
)

func setupTest(t *testing.T, startData string) (*core.Cluster, *gorp.DbMap, http.Handler, *TestPolicyEnforcer) {
	//load test database
	t.Helper()
	dbm := test.InitDatabase(t, &startData)

	//prepare test configuration
	sharedRatesThatReportUsage := []limesrates.RateInfo{
		//NOTE: MiB makes no sense for for this rate, but I want to test as many
		//combinations of "has unit or not", "has limit or not" and "has usage or
		//not" as possible
		{Name: "service/shared/objects:delete", Unit: limes.UnitMebibytes},
		{Name: "service/shared/objects:unlimited", Unit: limes.UnitKibibytes},
	}
	unsharedRatesThatReportUsage := []limesrates.RateInfo{
		{Name: "service/unshared/instances:delete", Unit: limes.UnitNone},
	}

	quotaPlugins := map[string]core.QuotaPlugin{
		"shared":   test.NewPlugin("shared", sharedRatesThatReportUsage...),
		"unshared": test.NewPlugin("unshared", unsharedRatesThatReportUsage...),
	}
	hasCentralizedService := startData == "fixtures/start-data.sql"
	if hasCentralizedService {
		quotaPlugins["centralized"] = test.NewPlugin("centralized")
	}

	westConstraintSet := core.QuotaConstraintSet{
		Domains: map[string]core.QuotaConstraints{
			"france": {
				"shared": {
					"capacity": {Minimum: p2u64(10), Maximum: p2u64(123), Unit: limes.UnitBytes},
					"things":   {Minimum: p2u64(20)},
				},
				"unshared": {
					"capacity": {Maximum: p2u64(20), Unit: limes.UnitBytes},
					"things":   {Minimum: p2u64(20), Maximum: p2u64(20)},
				},
			},
		},
		Projects: map[string]map[string]core.QuotaConstraints{
			"germany": {
				"berlin": {
					//This constraint is used for the happy-path tests, where PUT
					//succeeds because the requested value fits within the constraint.
					"shared": {"capacity": {Minimum: p2u64(1), Maximum: p2u64(12), Unit: limes.UnitBytes}},
				},
				"dresden": {
					//These constraints are used for the failure tests, where PUT fails
					//because the requested values conflict with the constraint.
					"shared": {
						"capacity": {Minimum: p2u64(10), Unit: limes.UnitBytes},
					},
					"unshared": {
						"capacity": {Minimum: p2u64(10), Maximum: p2u64(10), Unit: limes.UnitBytes},
						"things":   {Maximum: p2u64(10)},
					},
				},
			},
		},
	}

	var clusterConfig core.ClusterConfiguration
	if startData != "fixtures/start-data-inconsistencies.sql" {
		clusterConfig.Services = []core.ServiceConfiguration{
			{
				Type: "shared",
				RateLimits: core.ServiceRateLimitConfiguration{
					Global: []core.RateLimitConfiguration{
						{
							Name:   "service/shared/objects:create",
							Unit:   limes.UnitNone,
							Limit:  5000,
							Window: 1 * limesrates.WindowSeconds,
						},
					},
					ProjectDefault: []core.RateLimitConfiguration{
						{
							Name:   "service/shared/objects:create",
							Unit:   limes.UnitNone,
							Limit:  5,
							Window: 1 * limesrates.WindowMinutes,
						},
						{
							Name:   "service/shared/objects:delete",
							Unit:   limes.UnitNone,
							Limit:  1,
							Window: 1 * limesrates.WindowMinutes,
						},
						{
							Name:   "service/shared/objects:update",
							Unit:   limes.UnitNone,
							Limit:  2,
							Window: 1 * limesrates.WindowSeconds,
						},
						{
							Name:   "service/shared/objects:read/list",
							Unit:   limes.UnitNone,
							Limit:  3,
							Window: 1 * limesrates.WindowSeconds,
						},
					},
				},
			},
			{
				Type: "unshared",
				RateLimits: core.ServiceRateLimitConfiguration{
					ProjectDefault: []core.RateLimitConfiguration{
						{
							Name:   "service/unshared/instances:create",
							Unit:   limes.UnitNone,
							Limit:  5,
							Window: 1 * limesrates.WindowMinutes,
						},
						{
							Name:   "service/unshared/instances:delete",
							Unit:   limes.UnitNone,
							Limit:  1,
							Window: 1 * limesrates.WindowMinutes,
						},
						{
							Name:   "service/unshared/instances:update",
							Unit:   limes.UnitNone,
							Limit:  2,
							Window: 1 * limesrates.WindowSeconds,
						},
					},
				},
			},
		}
		if hasCentralizedService {
			clusterConfig.Services = append(clusterConfig.Services, core.ServiceConfiguration{Type: "centralized"})
		}
	}

	cluster := &core.Cluster{
		Auth:            &core.AuthSession{},
		DiscoveryPlugin: test.NewDiscoveryPlugin(),
		QuotaPlugins:    quotaPlugins,
		CapacityPlugins: map[string]core.CapacityPlugin{},
		Config:          clusterConfig,
	}
	if startData != "fixtures/start-data-inconsistencies.sql" {
		cluster.QuotaConstraints = &westConstraintSet
	}

	//load mock policy (where everything is allowed)
	enforcer := &TestPolicyEnforcer{
		AllowRaise:            true,
		AllowRaiseLP:          true,
		AllowLower:            true,
		AllowRaiseCentralized: true,
		AllowLowerCentralized: true,
	}
	cluster.Auth.TokenValidator = TestTokenValidator{enforcer}

	if startData != "fixtures/start-data-inconsistencies.sql" {
		cluster.Config.ResourceBehaviors = []*core.ResourceBehaviorConfiguration{
			//check minimum non-zero project quota constraint
			{
				Compiled: core.ResourceBehavior{
					MaxBurstMultiplier:     limesresources.BurstingMultiplier(math.Inf(+1)),
					FullResourceNameRx:     regexp.MustCompile("^unshared/things$"),
					MinNonZeroProjectQuota: 10,
				},
			},
			//check how scaling relations are reported
			{
				Compiled: core.ResourceBehavior{
					MaxBurstMultiplier:     limesresources.BurstingMultiplier(math.Inf(+1)),
					FullResourceNameRx:     regexp.MustCompile("^unshared/things$"),
					ScalesWithResourceName: "things",
					ScalesWithServiceType:  "shared",
					ScalingFactor:          2,
				},
			},
			//check how annotations are reported
			{
				Compiled: core.ResourceBehavior{
					MaxBurstMultiplier: limesresources.BurstingMultiplier(math.Inf(+1)),
					FullResourceNameRx: regexp.MustCompile("^shared/.*things$"),
					ScopeRx:            regexp.MustCompile("^germany(?:/dresden)?$"),
					Annotations: map[string]interface{}{
						"annotated": true,
						"text":      "this annotation appears on shared things of domain germany and project dresden",
					},
				},
			},
			{
				Compiled: core.ResourceBehavior{
					MaxBurstMultiplier: limesresources.BurstingMultiplier(math.Inf(+1)),
					FullResourceNameRx: regexp.MustCompile("^shared/things$"),
					ScopeRx:            regexp.MustCompile("^germany/dresden$"),
					Annotations: map[string]interface{}{
						"text": "this annotation appears on shared/things of project dresden only",
					},
				},
			},
		}
		cluster.Config.QuotaDistributionConfigs = []*core.QuotaDistributionConfiguration{
			//check behavior for centralized quota distribution (all other resources default to hierarchical quota distribution)
			{
				FullResourceNameRx:  regexp.MustCompile("^centralized/capacity$"),
				Model:               limesresources.CentralizedQuotaDistribution,
				DefaultProjectQuota: 15,
			},
			{
				FullResourceNameRx:  regexp.MustCompile("^centralized/things$"),
				Model:               limesresources.CentralizedQuotaDistribution,
				DefaultProjectQuota: 10,
			},
		}
	}

	//validate that this is a no-op when no OPAConfiguration is provided
	cluster.SetupOPA("", "")

	handler := httpapi.Compose(
		NewV1API(cluster, dbm),
		httpapi.WithGlobalMiddleware(ForbidClusterIDHeader),
		httpapi.WithoutLogging(),
	)
	return cluster, dbm, handler, enforcer
}

type TestPolicyEnforcer struct {
	AllowRaise            bool
	AllowRaiseLP          bool
	AllowLower            bool
	AllowRaiseCentralized bool
	AllowLowerCentralized bool
	RejectServiceType     string
}

// Enforce implements the gopherpolicy.Enforcer interface.
func (e TestPolicyEnforcer) Enforce(rule string, ctx policy.Context) bool {
	if e.RejectServiceType != "" && ctx.Request["service_type"] == e.RejectServiceType {
		return false
	}
	fields := strings.Split(rule, ":")
	switch fields[len(fields)-1] {
	case "raise":
		return e.AllowRaise
	case "raise_lowpriv":
		return e.AllowRaiseLP
	case "raise_centralized":
		return e.AllowRaiseCentralized
	case "lower":
		return e.AllowLower
	case "lower_centralized":
		return e.AllowLowerCentralized
	default:
		return true
	}
}

type TestTokenValidator struct {
	Enforcer gopherpolicy.Enforcer
}

// CheckToken implements the gopherpolicy.Validator interface.
func (v TestTokenValidator) CheckToken(r *http.Request) *gopherpolicy.Token {
	return &gopherpolicy.Token{
		Enforcer: v.Enforcer,
		Context: policy.Context{
			Request: map[string]string{}, //needs to be non-nil because fields are set later
		},
	}
}

func Test_InconsistencyOperations(t *testing.T) {
	_, _, router, _ := setupTest(t, "fixtures/start-data-inconsistencies.sql")

	//check ListInconsistencies
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/inconsistencies",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/inconsistency-list.json"),
	}.Check(t, router)
}

func Test_EmptyInconsistencyReport(t *testing.T) {
	_, _, router, _ := setupTest(t, "/dev/null")

	//check ListInconsistencies
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/inconsistencies",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/inconsistency-empty.json"),
	}.Check(t, router)
}

func Test_ScrapeErrorOperations(t *testing.T) {
	_, dbm, router, _ := setupTest(t, "fixtures/start-data.sql")

	//Add a scrape error to one specific service with type 'unshared'.
	_, err := dbm.Exec(`UPDATE project_services SET scrape_error_message = $1 WHERE id = $2 AND type = $3`,
		"could not scrape this specific unshared service",
		1, "unshared",
	)
	if err != nil {
		t.Fatal(err)
	}

	//Add the same scrape error to all services with type 'shared'. This will ensure that
	//they get grouped under a dummy project.
	_, err = dbm.Exec(`UPDATE project_services SET scrape_error_message = $1 WHERE type = $2`,
		"could not scrape shared service",
		"shared",
	)
	if err != nil {
		t.Fatal(err)
	}

	//check ListScrapeErrors
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/admin/scrape-errors",
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/scrape-error-list.json"),
	}.Check(t, router)
}

func Test_EmptyScrapeErrorReport(t *testing.T) {
	_, _, router, _ := setupTest(t, "/dev/null")

	//check ListScrapeErrors
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/admin/scrape-errors",
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/scrape-error-empty.json"),
	}.Check(t, router)
}

func Test_RateScrapeErrorOperations(t *testing.T) {
	_, dbm, router, _ := setupTest(t, "fixtures/start-data.sql")

	//Add a scrape error to one specific service with type 'unshared' that has rate data.
	_, err := dbm.Exec(`UPDATE project_services SET rates_scrape_error_message = $1 WHERE id = $2 AND type = $3`,
		"could not scrape rate data for this specific unshared service",
		1, "unshared",
	)
	if err != nil {
		t.Fatal(err)
	}

	//Add the same scrape error to both services with type 'shared' that have rate data.
	//This will ensure that they get grouped under a dummy project.
	_, err = dbm.Exec(`UPDATE project_services SET rates_scrape_error_message = $1 WHERE (id = $2 OR id = $3) AND type = $4`,
		"could not scrape rate data for shared service",
		2, 4, "shared",
	)
	if err != nil {
		t.Fatal(err)
	}

	//check ListRateScrapeErrors
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/rates/v1/admin/scrape-errors",
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/rate-scrape-error-list.json"),
	}.Check(t, router)
}

func Test_EmptyRateScrapeErrorReport(t *testing.T) {
	_, _, router, _ := setupTest(t, "/dev/null")

	//check ListRateScrapeErrors
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/rates/v1/admin/scrape-errors",
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/rate-scrape-error-empty.json"),
	}.Check(t, router)
}

func Test_ClusterOperations(t *testing.T) {
	cluster, _, router, _ := setupTest(t, "fixtures/start-data.sql")

	//check GetCluster
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/clusters/current",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("fixtures/cluster-get-west.json"),
	}.Check(t, router)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/clusters/current",
		Header:       map[string]string{"X-Limes-Cluster-Id": "current"}, //still allowed for backwards compatibility
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("fixtures/cluster-get-west.json"),
	}.Check(t, router)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/clusters/current?service=unknown",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("fixtures/cluster-get-west-no-services.json"),
	}.Check(t, router)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/clusters/current?service=shared&resource=unknown",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("fixtures/cluster-get-west-no-resources.json"),
	}.Check(t, router)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/clusters/current?service=shared&resource=things",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("fixtures/cluster-get-west-filtered.json"),
	}.Check(t, router)

	//check GetClusterRates
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/rates/v1/clusters/current",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("fixtures/cluster-get-west-only-rates.json"),
	}.Check(t, router)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/rates/v1/clusters/current?rates",
		ExpectStatus: 400,
		ExpectBody:   assert.StringData("the `rates` query parameter is not allowed here\n"),
	}.Check(t, router)

	//check rendering of overcommit factors
	cluster.Config.ResourceBehaviors = []*core.ResourceBehaviorConfiguration{
		{
			Compiled: core.ResourceBehavior{
				FullResourceNameRx: regexp.MustCompile("^shared/things$"),
				MaxBurstMultiplier: limesresources.BurstingMultiplier(math.Inf(+1)),
				OvercommitFactor:   2.5,
			},
		},
		{
			Compiled: core.ResourceBehavior{
				FullResourceNameRx: regexp.MustCompile("^unshared/things$"),
				MaxBurstMultiplier: limesresources.BurstingMultiplier(math.Inf(+1)),
				OvercommitFactor:   1.5,
			},
		},
	}
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/clusters/current",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("fixtures/cluster-get-west-with-overcommit.json"),
	}.Check(t, router)
}

func Test_DomainOperations(t *testing.T) {
	cluster, dbm, router, _ := setupTest(t, "fixtures/start-data.sql")
	discovery := cluster.DiscoveryPlugin.(*test.DiscoveryPlugin) //nolint:errcheck

	//check GetDomain
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/domain-get-germany.json"),
	}.Check(t, router)
	//domain "france" covers some special cases: an infinite backend quota and
	//missing domain quota entries for one service
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-france",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/domain-get-france.json"),
	}.Check(t, router)

	//check ListDomains
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/domain-list.json"),
	}.Check(t, router)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains?service=unknown",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/domain-list-no-services.json"),
	}.Check(t, router)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains?service=shared&resource=unknown",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/domain-list-no-resources.json"),
	}.Check(t, router)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains?service=shared&resource=things",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/domain-list-filtered.json"),
	}.Check(t, router)

	//check cross-cluster ListDomains
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains",
		Header:       map[string]string{"X-Limes-Cluster-Id": "unknown"},
		ExpectStatus: 400,
		ExpectBody:   assert.StringData("multi-cluster support is removed: the X-Limes-Cluster-Id header is not allowed anymore\n"),
	}.Check(t, router)

	//check DiscoverDomains
	discovery.StaticDomains = append(discovery.StaticDomains,
		core.KeystoneDomain{Name: "spain", UUID: "uuid-for-spain"},
	)
	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/discover",
		ExpectStatus: 202,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/domain-discover.json"),
	}.Check(t, router)

	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/discover",
		ExpectStatus: 204, //no content because no new domains discovered
		ExpectBody:   assert.StringData(""),
	}.Check(t, router)

	//check PutDomain error cases
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany",
		ExpectStatus: 409,
		ExpectBody:   assert.StringData("cannot change shared/capacity quota: domain quota may not be smaller than sum of project quotas in that domain (minimum acceptable domain quota is 20 B)\n"),
		//should fail because project quota sum exceeds new quota
		Body: requestOneQuotaChange("domain", "shared", "capacity", 1, limes.UnitNone),
	}.Check(t, router)
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany",
		ExpectStatus: 422,
		ExpectBody:   assert.StringData("cannot change shared/things quota: cannot convert value from MiB to <count> because units are incompatible\n"),
		//should fail because unit is incompatible with resource
		Body: requestOneQuotaChange("domain", "shared", "things", 1, "MiB"),
	}.Check(t, router)

	//check PutDomain error cases because of constraints
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-france",
		ExpectStatus: 422,
		ExpectBody:   assert.StringData("cannot change shared/things quota: requested value \"15\" contradicts constraint \"at least 20\" for this domain and resource (minimum acceptable domain quota is 20)\ncannot change unshared/capacity quota: requested value \"30 B\" contradicts constraint \"at most 20 B\" for this domain and resource (maximum acceptable domain quota is 20 B)\ncannot change unshared/things quota: requested value \"19\" contradicts constraint \"exactly 20\" for this domain and resource (minimum acceptable domain quota is 20, maximum acceptable domain quota is 20)\n"),
		Body: assert.JSONObject{
			"domain": assert.JSONObject{
				"services": []assert.JSONObject{
					{
						"type": "shared",
						"resources": []assert.JSONObject{
							//should fail because of "at least 20" constraint
							{"name": "things", "quota": 15},
						},
					},
					{
						"type": "unshared",
						"resources": []assert.JSONObject{
							//should fail because of "at most 20" constraint
							{"name": "capacity", "quota": 30},
							//should fail because of "exactly 20" constraint
							{"name": "things", "quota": 19},
						},
					},
				},
			},
		},
	}.Check(t, router)

	//check PutDomain with bogus service types and resource names
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany",
		ExpectStatus: 422,
		ExpectBody:   assert.StringData("cannot change unknown/things quota: no such service\n"),
		Body:         requestOneQuotaChange("domain", "unknown", "things", 100, limes.UnitNone),
	}.Check(t, router)
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany",
		ExpectStatus: 422,
		ExpectBody:   assert.StringData("cannot change shared/unknown quota: no such resource\n"),
		Body:         requestOneQuotaChange("domain", "shared", "unknown", 100, limes.UnitNone),
	}.Check(t, router)

	//check PutDomain with resource that does not track quota
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany",
		ExpectStatus: 403,
		ExpectBody:   assert.StringData("cannot change shared/capacity_portion quota: resource does not track quota\n"),
		Body:         requestOneQuotaChange("domain", "shared", "capacity_portion", 1, limes.UnitNone),
	}.Check(t, router)

	//check PutDomain happy path
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany",
		ExpectStatus: 202,
		Body:         requestOneQuotaChange("domain", "shared", "capacity", 1234, limes.UnitNone),
	}.Check(t, router)
	expectDomainQuota(t, dbm, "germany", "shared", "capacity", 1234)
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany",
		ExpectStatus: 202,
		Body:         requestOneQuotaChange("domain", "shared", "capacity", 1, limes.UnitMebibytes),
	}.Check(t, router)
	expectDomainQuota(t, dbm, "germany", "shared", "capacity", 1<<20)

	//check a bizarre edge case that was going wrong at some point: when
	//setting the quota to `<value> <unit>` where the current quota was `<value>
	//<other-unit>` (with the same value, but a different unit), the update would
	//be skipped because the value was compared before the unit conversion
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany",
		ExpectStatus: 202,
		Body:         requestOneQuotaChange("domain", "shared", "capacity", 1<<20, limes.UnitKibibytes),
	}.Check(t, router)
	expectDomainQuota(t, dbm, "germany", "shared", "capacity", 1<<30)

	//check PutDomain on a missing domain quota (see issue #36)
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-france",
		ExpectStatus: 202,
		Body:         requestOneQuotaChange("domain", "shared", "capacity", 123, limes.UnitNone),
	}.Check(t, router)
	expectDomainQuota(t, dbm, "france", "shared", "capacity", 123)

	//check SimulatePutDomain for no actual changes (all quotas requested already are set like that)
	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/uuid-for-germany/simulate-put",
		ExpectStatus: 200,
		Body:         requestOneQuotaChange("domain", "shared", "capacity", 1<<20, limes.UnitKibibytes),
		ExpectBody: assert.JSONObject{
			"success": true,
		},
	}.Check(t, router)

	//check SimulatePutDomain for acceptable changes
	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/uuid-for-germany/simulate-put",
		ExpectStatus: 200,
		Body:         requestOneQuotaChange("domain", "shared", "capacity", 1, limes.UnitMebibytes),
		ExpectBody: assert.JSONObject{
			"success": true,
		},
	}.Check(t, router)

	//check SimulatePutDomain for partially unacceptable changes
	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/uuid-for-germany/simulate-put",
		ExpectStatus: 200,
		Body: assert.JSONObject{
			"domain": assert.JSONObject{
				"services": []assert.JSONObject{
					{
						"type": "shared",
						"resources": []assert.JSONObject{
							{"name": "capacity", "quota": 100},
							//should fail with 422 because of incompatible units
							{"name": "things", "quota": 1, "unit": limes.UnitBytes},
						},
					},
					{
						"type": "unshared",
						"resources": []assert.JSONObject{
							//should fail with 409 because project quotas are higher than that
							{"name": "capacity", "quota": 0},
							{"name": "things", "quota": 100},
						},
					},
				},
			},
		},
		ExpectBody: assert.JSONObject{
			"success": false,
			"unacceptable_resources": []assert.JSONObject{
				{
					"service_type":  "shared",
					"resource_name": "things",
					"status":        422,
					"message":       `cannot convert value from B to <count> because units are incompatible`,
				},
				{
					"service_type":         "unshared",
					"resource_name":        "capacity",
					"status":               409,
					"message":              "domain quota may not be smaller than sum of project quotas in that domain",
					"min_acceptable_quota": 20,
					"unit":                 "B",
				},
			},
		},
	}.Check(t, router)

	//When burst is in use, the sum of all project quotas may legitimately be
	//higher than the domain quota.  In this case, the validation that
	//domainQuota >= sum(projectQuota) should only produce an error when
	//*decreasing* quota, not when increasing it. In other words, it should be
	//allowed to decrease burst usage even if it is not possible to completely
	//eliminate it.
	domainGermanyID, err := dbm.SelectInt(`SELECT id FROM domains WHERE name = $1`,
		"germany")
	if err != nil {
		t.Fatal(err)
	}
	serviceGermanyUnsharedID, err := dbm.SelectInt(`SELECT ID from domain_services WHERE domain_id = $1 AND type = $2`,
		domainGermanyID, "unshared")
	if err != nil {
		t.Fatal(err)
	}
	_, err = dbm.Exec(`UPDATE domain_resources SET quota = $1 WHERE service_id = $2 AND name = $3`,
		10, //but sum(projectQuota) = 20!
		serviceGermanyUnsharedID, "capacity",
	)
	if err != nil {
		t.Fatal(err)
	}
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany",
		ExpectStatus: 202,
		//less than sum(projectQuota), but more than before, so it's okay
		Body: requestOneQuotaChange("domain", "unshared", "capacity", 15, limes.UnitNone),
	}.Check(t, router)
}

func Test_DomainOPA(t *testing.T) {
	cluster, _, router, _ := setupTest(t, "fixtures/start-data-opa.sql")
	cluster.SetupOPA("fixtures/limes.rego", "fixtures/limes.rego")

	// try if valid operations still work
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany",
		ExpectStatus: 202,
		ExpectBody:   assert.StringData(""),
		Body: assert.JSONObject{
			"domain": assert.JSONObject{
				"services": []assert.JSONObject{
					{
						"type": "shared",
						"resources": []assert.JSONObject{
							{"name": "capacity", "quota": 30},
							{"name": "things", "quota": 20},
						},
					},
				},
			},
		},
	}.Check(t, router)

	//same as above, but we don't include the "shared/things" quota value in the
	//request (this is to prove fixing of a bug where all quotas not supplied in
	//the request were set to 0 in the policy input data)
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany",
		ExpectStatus: 202,
		ExpectBody:   assert.StringData(""),
		Body:         requestOneQuotaChange("domain", "shared", "capacity", 30, limes.UnitNone),
	}.Check(t, router)

	// try invalid operations which should trigger a violation
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany",
		ExpectStatus: 422,
		ExpectBody:   assert.StringData("cannot change shared/capacity quota: must allocate shared/things quota before\n"),
		Body: assert.JSONObject{
			"domain": assert.JSONObject{
				"services": []assert.JSONObject{
					{
						"type": "shared",
						"resources": []assert.JSONObject{
							{"name": "things", "quota": 0},
							{"name": "capacity", "quota": 30},
						},
					},
				},
			},
		},
	}.Check(t, router)
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany",
		ExpectStatus: 422,
		ExpectBody:   assert.StringData("cannot change shared/capacity quota: must not allocate shared/capacity and unshared/capacity at the same time\n"),
		Body: assert.JSONObject{
			"domain": assert.JSONObject{
				"services": []assert.JSONObject{
					{
						"type": "shared",
						"resources": []assert.JSONObject{
							{"name": "capacity", "quota": 35},
						},
					},
					{
						"type": "unshared",
						"resources": []assert.JSONObject{
							{"name": "capacity", "quota": 35},
						},
					},
				},
			},
		},
	}.Check(t, router)
}

// Even though serviceType parameter always receives "shared" but this is
// intentional as it improves code readability. Additionally, this behavior could change
// with future unit tests.
//
//nolint:unparam
func expectDomainQuota(t *testing.T, dbm *gorp.DbMap, domainName, serviceType, resourceName string, expected uint64) {
	t.Helper()

	var actualQuota uint64
	err := dbm.QueryRow(`
		SELECT dr.quota FROM domain_resources dr
		JOIN domain_services ds ON ds.id = dr.service_id
		JOIN domains d ON d.id = ds.domain_id
		WHERE d.name = $1 AND ds.type = $2 AND dr.name = $3`,
		domainName, serviceType, resourceName).Scan(&actualQuota)
	if err != nil {
		t.Fatal(err)
	}
	if actualQuota != expected {
		t.Errorf(
			"domain quota for %s/%s/%s was not updated in database",
			domainName, serviceType, resourceName,
		)
	}
}

func Test_ProjectOperations(t *testing.T) {
	cluster, dbm, router, _ := setupTest(t, "fixtures/start-data.sql")
	discovery := cluster.DiscoveryPlugin.(*test.DiscoveryPlugin) //nolint:errcheck

	//check GetProject
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-get-berlin.json"),
	}.Check(t, router)
	//check rendering of subresources
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin?detail",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-get-details-berlin.json"),
	}.Check(t, router)
	//dresden has a case of backend quota != quota
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-dresden",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-get-dresden.json"),
	}.Check(t, router)
	//paris has a case of infinite backend quota
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-france/projects/uuid-for-paris",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-get-paris.json"),
	}.Check(t, router)

	//check GetProjectRates
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/rates/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-get-berlin-only-rates.json"),
	}.Check(t, router)
	//dresden has some rates that only report usage
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/rates/v1/domains/uuid-for-germany/projects/uuid-for-dresden",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-get-dresden-only-rates.json"),
	}.Check(t, router)
	//paris has no rates in the DB whatsoever, so we can check the rendering of the default rates
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/rates/v1/domains/uuid-for-france/projects/uuid-for-paris",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-get-paris-only-default-rates.json"),
	}.Check(t, router)

	//check non-existent domains/projects
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-switzerland/projects/uuid-for-bern",
		ExpectStatus: 404,
		ExpectBody:   assert.StringData("no such domain (if it was just created, try to POST /domains/discover)\n"),
	}.Check(t, router)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-hamburg",
		ExpectStatus: 404,
		ExpectBody:   assert.StringData("no such project (if it was just created, try to POST /domains/uuid-for-germany/projects/discover)\n"),
	}.Check(t, router)

	//check ListProjects
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-list.json"),
	}.Check(t, router)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects?service=unknown",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-list-no-services.json"),
	}.Check(t, router)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects?service=shared&resource=unknown",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-list-no-resources.json"),
	}.Check(t, router)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects?service=shared&resource=things",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-list-filtered.json"),
	}.Check(t, router)

	//check ListProjectRates
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/rates/v1/domains/uuid-for-germany/projects",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-list-only-rates.json"),
	}.Check(t, router)

	//check ?area= filter (esp. interaction with ?service= filter)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects?area=unknown",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-list-no-services.json"),
	}.Check(t, router)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects?area=shared&service=unshared",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-list-no-services.json"),
	}.Check(t, router)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects?area=shared&resource=things",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-list-filtered.json"),
	}.Check(t, router)

	//check DiscoverProjects
	discovery.StaticProjects["uuid-for-germany"] = append(discovery.StaticProjects["uuid-for-germany"],
		core.KeystoneProject{Name: "frankfurt", UUID: "uuid-for-frankfurt"},
	)
	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/uuid-for-germany/projects/discover",
		ExpectStatus: 202,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-discover.json"),
	}.Check(t, router)

	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/uuid-for-germany/projects/discover",
		ExpectStatus: 204, //no content because no new projects discovered
		ExpectBody:   assert.StringData(""),
	}.Check(t, router)

	//check SyncProject
	expectStaleProjectServices(t, dbm, "stale" /*, nothing */)
	expectStaleProjectServices(t, dbm, "rates_stale" /*, nothing */)
	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-dresden/sync",
		ExpectStatus: 202,
		ExpectBody:   assert.StringData(""),
	}.Check(t, router)
	expectStaleProjectServices(t, dbm, "stale", "dresden:centralized", "dresden:shared", "dresden:unshared")
	expectStaleProjectServices(t, dbm, "rates_stale" /*, nothing */)

	//SyncProject should discover the given project if not yet done
	discovery.StaticProjects["uuid-for-germany"] = append(discovery.StaticProjects["uuid-for-germany"],
		core.KeystoneProject{Name: "walldorf", UUID: "uuid-for-walldorf", ParentUUID: "uuid-for-germany"},
	)
	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-walldorf/sync",
		ExpectStatus: 202,
		ExpectBody:   assert.StringData(""),
	}.Check(t, router)
	expectStaleProjectServices(t, dbm, "stale", "dresden:centralized", "dresden:shared", "dresden:unshared", "walldorf:centralized", "walldorf:shared", "walldorf:unshared")

	//check SyncProjectRates (we don't need to check discovery again since SyncProjectRates shares this part of the
	//implementation with SyncProject)
	_, _ = dbm.Exec(`UPDATE project_services SET stale = 'f'`) //nolint:errcheck
	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/rates/v1/domains/uuid-for-germany/projects/uuid-for-dresden/sync",
		ExpectStatus: 202,
		ExpectBody:   assert.StringData(""),
	}.Check(t, router)
	expectStaleProjectServices(t, dbm, "stale" /*, nothing */)
	expectStaleProjectServices(t, dbm, "rates_stale", "dresden:centralized", "dresden:shared", "dresden:unshared")

	//check GetProject for a project that has been discovered, but not yet synced
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-walldorf",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-get-walldorf-not-scraped-yet.json"),
	}.Check(t, router)

	//check PutProject: pre-flight checks
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 409,
		ExpectBody:   assert.StringData("cannot change shared/capacity quota: quota may not be lower than current usage (minimum acceptable project quota is 2 B)\ncannot change shared/things quota: domain quota exceeded (maximum acceptable project quota is 20)\n"),
		Body: assert.JSONObject{
			"project": assert.JSONObject{
				"services": []assert.JSONObject{
					{
						"type": "shared",
						"resources": []assert.JSONObject{
							//should fail because usage exceeds new quota
							{"name": "capacity", "quota": 1},
							//should fail because domain quota exceeded
							{"name": "things", "quota": 30},
						},
					},
				},
			},
		},
	}.Check(t, router)

	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-dresden",
		ExpectStatus: 422,
		ExpectBody:   assert.StringData("cannot change shared/capacity quota: requested value \"9 B\" contradicts constraint \"at least 10 B\" for this project and resource (minimum acceptable project quota is 10 B)\ncannot change unshared/capacity quota: requested value \"20 B\" contradicts constraint \"exactly 10 B\" for this project and resource (minimum acceptable project quota is 10 B, maximum acceptable project quota is 10 B)\ncannot change unshared/things quota: requested value \"11\" contradicts constraint \"at most 10\" for this project and resource (maximum acceptable project quota is 10)\n"),
		Body: assert.JSONObject{
			"project": assert.JSONObject{
				"services": []assert.JSONObject{
					{
						"type": "shared",
						"resources": []assert.JSONObject{
							//should fail because of "at least 10" constraint
							{"name": "capacity", "quota": 9},
						},
					},
					{
						"type": "unshared",
						"resources": []assert.JSONObject{
							//should fail because of "exactly 10" constraint
							{"name": "capacity", "quota": 20},
							//should fail because of "at most 10" constraint
							{"name": "things", "quota": 11},
						},
					},
				},
			},
		},
	}.Check(t, router)

	plugin := cluster.QuotaPlugins["shared"].(*test.Plugin) //nolint:errcheck
	plugin.QuotaIsNotAcceptable = true
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 422,
		ExpectBody:   assert.StringData("cannot change shared/capacity quota: not acceptable for this project: IsQuotaAcceptableForProject failed as requested for quota set capacity=5, things=10\n"),
		Body:         requestOneQuotaChange("project", "shared", "capacity", 5, limes.UnitNone),
	}.Check(t, router)
	plugin.QuotaIsNotAcceptable = false

	//check PutProject: quota admissible (i.e. will be persisted in DB), but
	//SetQuota fails for some reason (e.g. backend service down)
	plugin.SetQuotaFails = true
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 202,
		ExpectBody:   assert.StringData("quotas have been accepted, but some error(s) occurred while trying to write the quotas into the backend services:\nSetQuota failed as requested\n"),
		Body:         requestOneQuotaChange("project", "shared", "capacity", 5, limes.UnitNone),
	}.Check(t, router)
	var (
		actualQuota        uint64
		actualBackendQuota uint64
	)
	err := dbm.QueryRow(`
		SELECT pr.quota, pr.backend_quota FROM project_resources pr
		JOIN project_services ps ON ps.id = pr.service_id
		JOIN projects p ON p.id = ps.project_id
		WHERE p.name = $1 AND ps.type = $2 AND pr.name = $3`,
		"berlin", "shared", "capacity").Scan(&actualQuota, &actualBackendQuota)
	if err != nil {
		t.Fatal(err)
	}
	if actualQuota != 5 {
		t.Error("quota was not updated in database")
	}
	if actualBackendQuota == 5 {
		t.Error("backend quota was updated in database even though SetQuota failed")
	}

	//check PutProject with bogus service types and resource names
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 422,
		ExpectBody:   assert.StringData("cannot change unknown/things quota: no such service\n"),
		Body:         requestOneQuotaChange("project", "unknown", "things", 100, limes.UnitNone),
	}.Check(t, router)
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 422,
		ExpectBody:   assert.StringData("cannot change shared/unknown quota: no such resource\n"),
		Body:         requestOneQuotaChange("project", "shared", "unknown", 100, limes.UnitNone),
	}.Check(t, router)

	//check PutProject with resource that does not track quota
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 403,
		ExpectBody:   assert.StringData("cannot change shared/capacity_portion quota: resource does not track quota\n"),
		Body:         requestOneQuotaChange("project", "shared", "capacity_portion", 1, limes.UnitNone),
	}.Check(t, router)

	//check PutProject happy path
	plugin.SetQuotaFails = false
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 202,
		Body:         requestOneQuotaChange("project", "shared", "capacity", 6, limes.UnitNone),
	}.Check(t, router)
	err = dbm.QueryRow(`
		SELECT pr.quota, pr.backend_quota FROM project_resources pr
		JOIN project_services ps ON ps.id = pr.service_id
		JOIN projects p ON p.id = ps.project_id
		WHERE p.name = $1 AND ps.type = $2 AND pr.name = $3`,
		"berlin", "shared", "capacity").Scan(&actualQuota, &actualBackendQuota)
	if err != nil {
		t.Fatal(err)
	}
	if actualQuota != 6 {
		t.Error("quota was not updated in database")
	}
	if actualBackendQuota != 6 {
		t.Error("backend quota was not updated in database")
	}
	//double-check with the plugin that the write was durable, and also that
	//SetQuota sent *all* quotas for that service (even for resources that were
	//not touched) as required by the QuotaPlugin interface documentation
	expectBackendQuota := map[string]uint64{
		"capacity": 6,  //as set above
		"things":   10, //unchanged
	}
	backendQuota, exists := plugin.OverrideQuota["uuid-for-berlin"]
	if !exists {
		t.Error("quota was not sent to backend")
	}
	if !reflect.DeepEqual(expectBackendQuota, backendQuota) {
		t.Errorf("expected backend quota %#v, but got %#v", expectBackendQuota, backendQuota)
	}

	//Check PUT ../project with rate limits.
	//Attempt setting a rate limit for which no default exists should fail.
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/rates/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 403,
		ExpectBody: assert.StringData(
			"cannot change shared/service/shared/notexistent:bogus rate limits: user is not allowed to create new rate limits\n",
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
	}.Check(t, router)
	var (
		actualLimit  uint64
		actualWindow limesrates.Window
	)
	err = dbm.QueryRow(`
		SELECT pra.rate_limit, pra.window_ns FROM project_rates pra
		JOIN project_services ps ON ps.id = pra.service_id
		JOIN projects p ON p.id = ps.project_id
		WHERE p.name = $1 AND ps.type = $2 AND pra.name = $3`,
		"berlin", "shared", "service/shared/notexistent:bogus").Scan(&actualLimit, &actualWindow)
	//There shouldn't be anything in the DB.
	if err.Error() != "sql: no rows in result set" {
		t.Fatalf("expected error %v but got %v", "sql: no rows in result set", err)
	}

	//Attempt setting a rate limit for which a default exists should be successful.
	rateName := "service/shared/objects:read/list"
	expectedLimit := uint64(100)
	expectedWindow := 1 * limesrates.WindowSeconds

	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/rates/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 202,
		Body: assert.JSONObject{
			"project": assert.JSONObject{
				"services": []assert.JSONObject{
					{
						"type": "shared",
						"rates": []assert.JSONObject{
							{
								"name":   rateName,
								"limit":  expectedLimit,
								"window": expectedWindow.String(),
							},
						},
					},
				},
			},
		},
	}.Check(t, router)

	err = dbm.QueryRow(`
		SELECT pra.rate_limit, pra.window_ns FROM project_rates pra
		JOIN project_services ps ON ps.id = pra.service_id
		JOIN projects p ON p.id = ps.project_id
		WHERE p.name = $1 AND ps.type = $2 AND pra.name = $3`,
		"berlin", "shared", rateName).Scan(&actualLimit, &actualWindow)
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

	//check SimulatePutProject for no actual changes (all quotas requested already are set like that)
	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/simulate-put",
		ExpectStatus: 200,
		Body:         requestOneQuotaChange("project", "shared", "capacity", 6, limes.UnitNone),
		ExpectBody: assert.JSONObject{
			"success": true,
		},
	}.Check(t, router)

	//check SimulatePutProject for acceptable changes (we have to set usage = 0
	//on the unshared/things resource to check setting quota to 0 successfully)
	domainGermanyID, err := dbm.SelectInt(`SELECT id FROM domains WHERE name = $1`,
		"germany")
	if err != nil {
		t.Fatal(err)
	}
	projectBerlinID, err := dbm.SelectInt(`SELECT id FROM projects WHERE domain_id = $1 AND name = $2`,
		domainGermanyID, "berlin")
	if err != nil {
		t.Fatal(err)
	}
	serviceBerlinUnsharedID, err := dbm.SelectInt(`SELECT ID from project_services WHERE project_id = $1 AND type = $2`,
		projectBerlinID, "unshared")
	if err != nil {
		t.Fatal(err)
	}
	//nolint:errcheck
	_, _ = dbm.Exec(`UPDATE project_resources SET usage = $1 WHERE service_id = $2 AND name = $3`,
		0,
		serviceBerlinUnsharedID, "things",
	)
	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/simulate-put",
		ExpectStatus: 200,
		Body: assert.JSONObject{
			"project": assert.JSONObject{
				"services": []assert.JSONObject{
					{
						"type": "shared",
						"resources": []assert.JSONObject{
							{"name": "capacity", "quota": 5},
						},
					},
					{
						"type": "unshared",
						"resources": []assert.JSONObject{
							//MinNonZeroProjectQuota should not block setting a quota to 0
							{"name": "things", "quota": 0},
						},
					},
				},
			},
		},
		ExpectBody: assert.JSONObject{
			"success": true,
		},
	}.Check(t, router)

	//check SimulatePutProject for partially unacceptable changes
	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/simulate-put",
		ExpectStatus: 200,
		Body: assert.JSONObject{
			"project": assert.JSONObject{
				"services": []assert.JSONObject{
					{
						"type": "shared",
						"resources": []assert.JSONObject{
							{"name": "capacity", "quota": 4},
							//should fail with 422 because of incompatible units
							{"name": "things", "quota": 1, "unit": limes.UnitBytes},
						},
					},
					{
						"type": "unshared",
						"resources": []assert.JSONObject{
							//should fail with 409 because usage is higher than that
							{"name": "capacity", "quota": 0},
							//should fail with 422 because of minimum non-zero project quota constraint
							{"name": "things", "quota": 4},
						},
					},
				},
			},
		},
		ExpectBody: assert.JSONObject{
			"success": false,
			"unacceptable_resources": []assert.JSONObject{
				{
					"service_type":  "shared",
					"resource_name": "things",
					"status":        422,
					"message":       `cannot convert value from B to <count> because units are incompatible`,
				},
				{
					"service_type":         "unshared",
					"resource_name":        "capacity",
					"status":               409,
					"message":              "quota may not be lower than current usage",
					"min_acceptable_quota": 2,
					"unit":                 "B",
				},
				{
					"service_type":         "unshared",
					"resource_name":        "things",
					"status":               422,
					"message":              "must allocate at least 10 quota",
					"min_acceptable_quota": 10,
				},
			},
		},
	}.Check(t, router)

	//When burst is in use, the project usage is legitimately higher than its
	//quota.  In this case, the validation that projectQuota >= projectUsage
	//should only produce an error when *decreasing* quota, not when increasing
	//it. In other words, it should be allowed to decrease burst usage even if it
	//is not possible to completely eliminate it.
	_, err = dbm.Exec(`UPDATE project_resources SET quota = $1 WHERE service_id = $2 AND name = $3`,
		0, //but usage = 2!
		serviceBerlinUnsharedID, "capacity",
	)
	if err != nil {
		t.Fatal(err)
	}
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 202,
		//less than usage, but more than before, so it's okay
		Body: requestOneQuotaChange("project", "unshared", "capacity", 1, limes.UnitNone),
	}.Check(t, router)

	tr, tr0 := easypg.NewTracker(t, dbm.Db)
	tr0.Ignore()

	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 202,
		//project quota rises from 15->30, and thus domain quota rises from 25->40
		Body: requestOneQuotaChange("project", "centralized", "things", 30, limes.UnitNone),
	}.Check(t, router)
	tr.DBChanges().AssertEqual(`
		UPDATE domain_resources SET quota = 40 WHERE service_id = 5 AND name = 'things';
		UPDATE project_resources SET quota = 30, backend_quota = 30, desired_backend_quota = 30 WHERE service_id = 7 AND name = 'things';
	`)

	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 202,
		//project quota falls again from 30->15, and thus domain quota falls back from 40->25
		Body: requestOneQuotaChange("project", "centralized", "things", 15, limes.UnitNone),
	}.Check(t, router)
	tr.DBChanges().AssertEqual(`
		UPDATE domain_resources SET quota = 25 WHERE service_id = 5 AND name = 'things';
		UPDATE project_resources SET quota = 15, backend_quota = 15, desired_backend_quota = 15 WHERE service_id = 7 AND name = 'things';
	`)
}

func Test_RaiseLowerPermissions(t *testing.T) {
	cluster, dbm, router, enforcer := setupTest(t, "fixtures/start-data.sql")

	//we're not testing this right now
	cluster.QuotaConstraints = nil

	//test that the correct 403 errors are generated for missing permissions
	//(the other testcases cover the happy paths for raising and lowering)
	enforcer.AllowRaise = false
	enforcer.AllowRaiseLP = true
	enforcer.AllowLower = true
	enforcer.AllowRaiseCentralized = false
	enforcer.AllowLowerCentralized = false

	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany",
		ExpectStatus: 403,
		ExpectBody:   assert.StringData("cannot change shared/capacity quota: user is not allowed to raise \"shared\" quotas in this domain\n"),
		Body: assert.JSONObject{
			"domain": assert.JSONObject{
				"services": []assert.JSONObject{
					{
						"type": "shared",
						"resources": []assert.JSONObject{
							//attempt to raise should fail because of lack of permissions
							{"name": "capacity", "quota": 30},
							//attempt to lower should be permitted (but will not be executed)
							{"name": "things", "quota": 25},
						},
					},
				},
			},
		},
	}.Check(t, router)
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 403,
		ExpectBody:   assert.StringData("cannot change shared/capacity quota: user is not allowed to raise \"shared\" quotas in this project\n"),
		Body: assert.JSONObject{
			"project": assert.JSONObject{
				"services": []assert.JSONObject{
					{
						"type": "shared",
						"resources": []assert.JSONObject{
							//attempt to raise should fail because of lack of permissions
							{"name": "capacity", "quota": 11},
							//attempt to lower should be permitted (but will not be executed)
							{"name": "things", "quota": 5},
						},
					},
				},
			},
		},
	}.Check(t, router)

	enforcer.AllowRaise = true
	enforcer.AllowRaiseLP = true
	enforcer.AllowLower = false
	enforcer.AllowRaiseCentralized = false
	enforcer.AllowLowerCentralized = false

	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany",
		ExpectStatus: 403,
		ExpectBody:   assert.StringData("cannot change shared/things quota: user is not allowed to lower \"shared\" quotas in this domain\n"),
		Body: assert.JSONObject{
			"domain": assert.JSONObject{
				"services": []assert.JSONObject{
					{
						"type": "shared",
						"resources": []assert.JSONObject{
							//attempt to raise should be permitted (but will not be executed)
							{"name": "capacity", "quota": 30},
							//attempt to lower should fail because of lack of permissions
							{"name": "things", "quota": 25},
						},
					},
				},
			},
		},
	}.Check(t, router)
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 403,
		ExpectBody:   assert.StringData("cannot change shared/things quota: user is not allowed to lower \"shared\" quotas in this project\n"),
		Body: assert.JSONObject{
			"project": assert.JSONObject{
				"services": []assert.JSONObject{
					{
						"type": "shared",
						"resources": []assert.JSONObject{
							//attempt to raise should be permitted (but will not be executed)
							{"name": "capacity", "quota": 11},
							//attempt to lower should fail because of lack of permissions
							{"name": "things", "quota": 5},
						},
					},
				},
			},
		},
	}.Check(t, router)

	enforcer.AllowLower = true
	enforcer.RejectServiceType = "shared"

	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany",
		ExpectStatus: 403,
		ExpectBody:   assert.StringData("cannot change shared/things quota: user is not allowed to lower \"shared\" quotas in this domain\n"),
		Body: assert.JSONObject{
			"domain": assert.JSONObject{
				"services": []assert.JSONObject{
					{
						"type": "shared",
						"resources": []assert.JSONObject{
							//attempt to lower should fail because of lack of permissions for this service type
							{"name": "things", "quota": 25},
						},
					},
					{
						"type": "unshared",
						"resources": []assert.JSONObject{
							//attempt to lower should be permitted (but will not be executed)
							{"name": "capacity", "quota": 40},
						},
					},
				},
			},
		},
	}.Check(t, router)
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 403,
		ExpectBody:   assert.StringData("cannot change shared/things quota: user is not allowed to lower \"shared\" quotas in this project\n"),
		Body: assert.JSONObject{
			"project": assert.JSONObject{
				"services": []assert.JSONObject{
					{
						"type": "shared",
						"resources": []assert.JSONObject{
							//attempt to lower should fail because of lack of permissions for this service type
							{"name": "things", "quota": 5},
						},
					},
					{
						"type": "unshared",
						"resources": []assert.JSONObject{
							//attempt to lower should be permitted (but will not be executed)
							{"name": "capacity", "quota": 5},
						},
					},
				},
			},
		},
	}.Check(t, router)

	enforcer.AllowRaise = false
	enforcer.AllowRaiseLP = true
	enforcer.AllowLower = true
	enforcer.AllowRaiseCentralized = false
	enforcer.AllowLowerCentralized = false
	enforcer.RejectServiceType = ""

	cluster.LowPrivilegeRaise.LimitsForDomains = map[string]map[string]core.LowPrivilegeRaiseLimit{
		"shared": {"capacity": {AbsoluteValue: 29}, "things": {AbsoluteValue: 35}},
	}
	cluster.LowPrivilegeRaise.LimitsForProjects = map[string]map[string]core.LowPrivilegeRaiseLimit{
		"shared": {"capacity": {AbsoluteValue: 10}, "things": {AbsoluteValue: 25}},
	}

	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany",
		ExpectStatus: 403,
		ExpectBody:   assert.StringData("cannot change shared/capacity quota: user is not allowed to raise \"shared\" quotas that high in this domain (maximum acceptable domain quota is 29 B)\n"),
		Body: assert.JSONObject{
			"domain": assert.JSONObject{
				"services": []assert.JSONObject{
					{
						"type": "shared",
						"resources": []assert.JSONObject{
							//attempt to raise should fail because of lack of permissions
							{"name": "capacity", "quota": 30},
							//attempt to raise should be permitted by low-privilege exception
							{"name": "things", "quota": 35},
						},
					},
				},
			},
		},
	}.Check(t, router)
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 403,
		ExpectBody:   assert.StringData("cannot change shared/capacity quota: user is not allowed to raise \"shared\" quotas that high in this project (maximum acceptable project quota is 10 B)\n"),
		Body: assert.JSONObject{
			"project": assert.JSONObject{
				"services": []assert.JSONObject{
					{
						"type": "shared",
						"resources": []assert.JSONObject{
							//attempt to raise should fail because of lack of permissions
							{"name": "capacity", "quota": 11},
							//attempt to raise should be permitted by low-privilege exception
							{"name": "things", "quota": 11},
						},
					},
				},
			},
		},
	}.Check(t, router)

	cluster.Config.LowPrivilegeRaise.ExcludeProjectDomainRx = regexp.MustCompile(`germany`)

	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 403,
		ExpectBody:   assert.StringData("cannot change shared/capacity quota: user is not allowed to raise \"shared\" quotas in this project\ncannot change shared/things quota: user is not allowed to raise \"shared\" quotas in this project\n"),
		Body: assert.JSONObject{
			"project": assert.JSONObject{
				"services": []assert.JSONObject{
					{
						"type": "shared",
						"resources": []assert.JSONObject{
							//attempt to raise should fail because of lack of permissions
							{"name": "capacity", "quota": 11},
							//attempt to raise should fail because low-privilege q.r. is
							//disabled in this domain
							{"name": "things", "quota": 11},
						},
					},
				},
			},
		},
	}.Check(t, router)

	//test low-privilege raise limits that are specified as percent of cluster capacity
	cluster.Config.LowPrivilegeRaise.ExcludeProjectDomainRx = nil
	cluster.LowPrivilegeRaise.LimitsForDomains = map[string]map[string]core.LowPrivilegeRaiseLimit{
		// shared/things capacity is 246, so 13% is 31.98 which rounds down to 31
		"shared": {"things": {PercentOfClusterCapacity: 13}},
	}
	cluster.LowPrivilegeRaise.LimitsForProjects = map[string]map[string]core.LowPrivilegeRaiseLimit{
		// shared/things capacity is 246, so 5% is 12.3 which rounds down to 12
		"shared": {"things": {PercentOfClusterCapacity: 5}},
	}

	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany",
		ExpectStatus: 403,
		ExpectBody:   assert.StringData("cannot change shared/things quota: user is not allowed to raise \"shared\" quotas that high in this domain (maximum acceptable domain quota is 31)\n"),
		//attempt to raise should fail because low-privilege exception only applies up to 31
		Body: requestOneQuotaChange("domain", "shared", "things", 35, limes.UnitNone),
	}.Check(t, router)
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 403,
		ExpectBody:   assert.StringData("cannot change shared/things quota: user is not allowed to raise \"shared\" quotas that high in this project (maximum acceptable project quota is 12)\n"),
		//attempt to raise should fail because low-privilege exception only applies up to 12
		Body: requestOneQuotaChange("project", "shared", "things", 15, limes.UnitNone),
	}.Check(t, router)

	//test low-privilege raise limits that are specified as percentage of assigned cluster capacity over all domains
	cluster.LowPrivilegeRaise.LimitsForDomains = map[string]map[string]core.LowPrivilegeRaiseLimit{
		// - shared/things capacity is 246, 45% thereof is 110.7 which rounds down to 110
		// - current shared/things domain quotas: france = 0, germany = 30
		// -> germany should be able to go up to 80 before sum(domain quotas) exceeds 110
		"shared": {"things": {UntilPercentOfClusterCapacityAssigned: 45}},
	}

	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany",
		ExpectStatus: 403,
		ExpectBody:   assert.StringData("cannot change shared/things quota: user is not allowed to raise \"shared\" quotas that high in this domain (maximum acceptable domain quota is 110)\n"),
		//attempt to raise should fail because low-privilege exception only applies up to 110 (see comment above)
		Body: requestOneQuotaChange("domain", "shared", "things", 115, limes.UnitNone),
	}.Check(t, router)

	//raise another domain quota such that the auto-approval limit would be
	//exceeded even if the "germany" domain had zero quota
	domainFranceID, err := dbm.SelectInt(`SELECT id FROM domains WHERE name = $1`,
		"france")
	if err != nil {
		t.Fatal(err)
	}
	serviceFranceSharedID, err := dbm.SelectInt(`SELECT id FROM domain_services WHERE domain_id = $1 AND type = $2`,
		domainFranceID, "shared")
	if err != nil {
		t.Fatal(err)
	}
	_, err = dbm.Exec(`INSERT INTO domain_resources (service_id, name, quota) VALUES ($1, $2, $3)`,
		serviceFranceSharedID, "things",
		130, //more than the auto-approval limit of 110
	)
	if err != nil {
		t.Fatal(err)
	}

	//check a particular case that was going wrong at some point: when all
	//other domains together already exceed the auto-approval threshold, the
	//maximum acceptable domain quota is computed as negative and thus the
	//conversion to uint64 goes out of bounds if not checked properly
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany",
		ExpectStatus: 403,
		ExpectBody:   assert.StringData("cannot change shared/things quota: user is not allowed to raise \"shared\" quotas in this domain\n"),
		//attempt to raise should fail because low-privilege exception is not applicable because of other domain's quotas
		Body: requestOneQuotaChange("domain", "shared", "things", 35, limes.UnitNone),
	}.Check(t, router)

	//check that domain quota cannot be raised or lowered by anyone, even if the
	//policy says so, if the centralized quota distribution model is used
	enforcer.AllowRaise = true
	enforcer.AllowRaiseLP = true
	enforcer.AllowLower = true
	enforcer.AllowRaiseCentralized = true
	enforcer.AllowLowerCentralized = true

	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany",
		ExpectStatus: 403,
		ExpectBody:   assert.StringData("cannot change centralized/things quota: user is not allowed to raise \"centralized\" quotas in this domain\n"),
		Body:         requestOneQuotaChange("domain", "centralized", "things", 100, limes.UnitNone),
	}.Check(t, router)

	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany",
		ExpectStatus: 403,
		ExpectBody:   assert.StringData("cannot change centralized/things quota: user is not allowed to lower \"centralized\" quotas in this domain\n"),
		Body:         requestOneQuotaChange("domain", "centralized", "things", 0, limes.UnitNone),
	}.Check(t, router)

	//check that, under centralized quota distribution, project quota cannot be
	//raised or lowered if the respective specialized policies are not granted
	enforcer.AllowRaise = true
	enforcer.AllowRaiseLP = true
	enforcer.AllowLower = true
	enforcer.AllowRaiseCentralized = false
	enforcer.AllowLowerCentralized = true

	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 403,
		ExpectBody:   assert.StringData("cannot change centralized/things quota: user is not allowed to raise \"centralized\" quotas in this project\n"),
		Body:         requestOneQuotaChange("project", "centralized", "things", 100, limes.UnitNone),
	}.Check(t, router)

	enforcer.AllowRaiseCentralized = true
	enforcer.AllowLowerCentralized = false

	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 403,
		ExpectBody:   assert.StringData("cannot change centralized/things quota: user is not allowed to lower \"centralized\" quotas in this project\n"),
		Body:         requestOneQuotaChange("project", "centralized", "things", 5, limes.UnitNone),
	}.Check(t, router)

	//even if lower_centralized is allowed, lowering to 0 is never allowed (to
	//test this, we have to first set usage to 0 to avoid getting an error for
	//quota < usage instead)
	enforcer.AllowRaiseCentralized = true
	enforcer.AllowLowerCentralized = true

	_, err = dbm.Exec(`UPDATE project_resources SET usage = 0 WHERE service_id = 7 AND name = 'things'`)
	if err != nil {
		t.Error(err.Error())
	}

	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 422,
		ExpectBody:   assert.StringData("cannot change centralized/things quota: quota may not be lowered to zero for resources with non-zero default quota (minimum acceptable project quota is 1)\n"),
		Body:         requestOneQuotaChange("project", "centralized", "things", 0, limes.UnitNone),
	}.Check(t, router)
}

func expectStaleProjectServices(t *testing.T, dbm *gorp.DbMap, staleField string, pairs ...string) {
	t.Helper()

	queryStr := fmt.Sprintf(`
		SELECT p.name, ps.type
		  FROM projects p JOIN project_services ps ON ps.project_id = p.id
		 WHERE ps.%s
		 ORDER BY p.name, ps.type
	`, staleField)
	var actualPairs []string

	err := sqlext.ForeachRow(dbm, queryStr, nil, func(rows *sql.Rows) error {
		var (
			projectName string
			serviceType string
		)
		err := rows.Scan(&projectName, &serviceType)
		if err != nil {
			return err
		}
		actualPairs = append(actualPairs, fmt.Sprintf("%s:%s", projectName, serviceType))
		return nil
	})
	if err != nil {
		t.Fatal(err.Error())
	}

	if !reflect.DeepEqual(pairs, actualPairs) {
		t.Errorf("expected stale project services %v, but got %v", pairs, actualPairs)
	}
}

// p2u64 makes a "pointer to uint64".
func p2u64(val uint64) *uint64 {
	return &val
}

// p2i64 makes a "pointer to int64".
func p2i64(val int64) *int64 {
	return &val
}

func Test_QuotaBursting(t *testing.T) {
	cluster, dbm, router, _ := setupTest(t, "fixtures/start-data.sql")
	cluster.Config.Bursting.MaxMultiplier = 0.1

	//check initial GetProject with bursting disabled, but supported
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-get-berlin-bursting-disabled.json"),
	}.Check(t, router)

	//bursting can only enabled when the cluster supports it
	cluster.Config.Bursting.MaxMultiplier = 0
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 400,
		ExpectBody:   assert.StringData("bursting is not available for this cluster\n"),
		Body: assert.JSONObject{
			"project": assert.JSONObject{
				"bursting": assert.JSONObject{
					"enabled": true,
				},
			},
		},
	}.Check(t, router)

	//enable bursting
	cluster.Config.Bursting.MaxMultiplier = 0.1
	body := assert.JSONObject{
		"project": assert.JSONObject{
			"bursting": assert.JSONObject{
				"enabled": true,
			},
		},
	}
	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/simulate-put",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONObject{"success": true},
		Body:         body,
	}.Check(t, router)
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 202,
		ExpectBody:   assert.StringData(""),
		Body:         body,
	}.Check(t, router)

	//enabling bursting again should be a no-op
	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/simulate-put",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONObject{"success": true},
		Body:         body,
	}.Check(t, router)
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 202,
		ExpectBody:   assert.StringData(""),
		Body:         body,
	}.Check(t, router)

	//update a quota; this should also scale up the backend_quota according to
	//the bursting multiplier (we will check this in the next step)
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 202,
		ExpectBody:   assert.StringData(""),
		Body:         requestOneQuotaChange("project", "unshared", "things", 40, limes.UnitNone),
	}.Check(t, router)

	//check that quota has been updated in DB
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-get-berlin-bursting-enabled.json"),
	}.Check(t, router)

	//check that backend_quota has been updated in backend
	plugin := cluster.QuotaPlugins["shared"].(*test.Plugin) //nolint:errcheck
	expectBackendQuota := map[string]uint64{
		"capacity": 11, //original value (10) * multiplier (110%)
		"things":   11, //original value (10) * multiplier (110%)
	}
	backendQuota, exists := plugin.OverrideQuota["uuid-for-berlin"]
	if !exists {
		t.Error("quota was not sent to backend")
	}
	if !reflect.DeepEqual(expectBackendQuota, backendQuota) {
		t.Errorf("expected backend quota %#v, but got %#v", expectBackendQuota, backendQuota)
	}

	plugin = cluster.QuotaPlugins["unshared"].(*test.Plugin) //nolint:errcheck
	expectBackendQuota = map[string]uint64{
		"capacity": 11, //original value (10) * multiplier (110%)
		"things":   44, //as set above (40) * multiplier (110%)
	}
	backendQuota, exists = plugin.OverrideQuota["uuid-for-berlin"]
	if !exists {
		t.Error("quota was not sent to backend")
	}
	if !reflect.DeepEqual(expectBackendQuota, backendQuota) {
		t.Errorf("expected backend quota %#v, but got %#v", expectBackendQuota, backendQuota)
	}

	//check that resources with centralized quota distribution do not get burst quota applied
	tr, tr0 := easypg.NewTracker(t, dbm.Db)
	tr0.Ignore()
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 202,
		//project quota rises from 15->30, and thus domain quota rises from 25->40, but desired_backend_quota is also 30
		Body: requestOneQuotaChange("project", "centralized", "things", 30, limes.UnitNone),
	}.Check(t, router)
	tr.DBChanges().AssertEqual(`
		UPDATE domain_resources SET quota = 40 WHERE service_id = 5 AND name = 'things';
		UPDATE project_resources SET quota = 30, backend_quota = 30, desired_backend_quota = 30 WHERE service_id = 7 AND name = 'things';
	`)

	//revert the previous change, otherwise I would have to adjust every test fixture mentioned below
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 202,
		//project quota falls again from 30->15, and thus domain quota falls back from 40->25
		Body: requestOneQuotaChange("project", "centralized", "things", 15, limes.UnitNone),
	}.Check(t, router)
	tr.DBChanges().AssertEqual(`
		UPDATE domain_resources SET quota = 25 WHERE service_id = 5 AND name = 'things';
		UPDATE project_resources SET quota = 15, backend_quota = 15, desired_backend_quota = 15 WHERE service_id = 7 AND name = 'things';
	`)

	//increase usage beyond frontend quota -> should show up as burst usage
	_, err := dbm.Exec(`UPDATE project_resources SET usage = 42 WHERE service_id = 1 AND name = 'things'`)
	if err != nil {
		t.Error(err.Error())
	}
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-get-berlin-bursting-in-progress.json"),
	}.Check(t, router)

	//check that we cannot disable bursting now
	body = assert.JSONObject{
		"project": assert.JSONObject{
			"bursting": assert.JSONObject{
				"enabled": false,
			},
		},
	}
	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/simulate-put",
		ExpectStatus: 409,
		ExpectBody:   assert.StringData("cannot disable bursting because 1 resource is currently bursted: unshared/things\n"),
		Body:         body,
	}.Check(t, router)
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 409,
		ExpectBody:   assert.StringData("cannot disable bursting because 1 resource is currently bursted: unshared/things\n"),
		Body:         body,
	}.Check(t, router)

	//decrease usage, then disable bursting successfully
	_, err = dbm.Exec(`UPDATE project_resources SET usage = 2 WHERE service_id = 1 AND name = 'things'`)
	if err != nil {
		t.Error(err.Error())
	}
	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/simulate-put",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONObject{"success": true},
		Body:         body,
	}.Check(t, router)
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 202,
		ExpectBody:   assert.StringData(""),
		Body:         body,
	}.Check(t, router)

	//also resetting the quota that we changed above should bring us back into
	//the initial state
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 202,
		ExpectBody:   assert.StringData(""),
		Body:         requestOneQuotaChange("project", "unshared", "things", 10, limes.UnitNone),
	}.Check(t, router)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/project-get-berlin-bursting-disabled.json"),
	}.Check(t, router)

	//disabling bursting again should be a no-op
	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/simulate-put",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONObject{"success": true},
		Body:         body,
	}.Check(t, router)
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatus: 202,
		ExpectBody:   assert.StringData(""),
		Body:         body,
	}.Check(t, router)
}

func Test_EmptyProjectList(t *testing.T) {
	_, dbm, router, _ := setupTest(t, "fixtures/start-data.sql")

	_, err := dbm.Exec(`DELETE FROM projects`)
	if err != nil {
		t.Fatal(err)
	}

	//This warrants its own unit test since the rendering of empty project lists
	//uses a different code path than the rendering of non-empty project lists.
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONObject{"projects": []assert.JSONObject{}},
	}.Check(t, router)
}

func Test_LargeProjectList(t *testing.T) {
	//start without any projects pre-defined in the start data
	cluster, dbm, router, _ := setupTest(t, "fixtures/start-data-minimal.sql")
	//we don't care about the various ResourceBehaviors in this test
	cluster.Config.ResourceBehaviors = nil

	//template for how a single project will look in the output JSON
	makeProjectJSON := func(idx int, projectName, projectUUID string) assert.JSONObject {
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
							"quota_distribution_model": "hierarchical",
							"quota":                    0,
							"usable_quota":             0,
							"usage":                    0,
						},
						{
							"name":                     "things",
							"quota_distribution_model": "hierarchical",
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
							"quota_distribution_model": "hierarchical",
							"quota":                    0,
							"usable_quota":             0,
							"usage":                    0,
						},
						{
							"name":                     "things",
							"quota_distribution_model": "hierarchical",
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

	//set up a large number of projects to test the behavior of the project list endpoint for large lists
	projectCount := 100
	for idx := 1; idx <= projectCount; idx++ {
		projectUUIDGen, err := uuid.NewV4()
		if err != nil {
			t.Fatal(err)
		}
		projectName := fmt.Sprintf("test-project%04d", idx)
		projectUUID := projectUUIDGen.String()
		scrapedAt := time.Unix(int64(idx), 0).UTC()
		expectedProjectsJSON = append(expectedProjectsJSON, makeProjectJSON(idx, projectName, projectUUID))

		project := db.Project{
			DomainID:   1,
			ParentUUID: "uuid-for-germany",
			Name:       projectName,
			UUID:       projectUUID,
		}
		err = dbm.Insert(&project)
		if err != nil {
			t.Fatal(err)
		}
		for _, serviceType := range []string{"shared", "unshared"} {
			service := db.ProjectService{
				ProjectID:      project.ID,
				Type:           serviceType,
				ScrapedAt:      &scrapedAt,
				CheckedAt:      &scrapedAt,
				RatesScrapedAt: &scrapedAt,
				RatesCheckedAt: &scrapedAt,
			}
			err = dbm.Insert(&service)
			if err != nil {
				t.Fatal(err)
			}
			for _, resourceName := range []string{"things", "capacity"} {
				resource := db.ProjectResource{
					ServiceID:           service.ID,
					Name:                resourceName,
					Quota:               p2u64(0),
					BackendQuota:        p2i64(0),
					DesiredBackendQuota: p2u64(0),
				}
				if serviceType == "unshared" && resourceName == "things" {
					resource.Quota = p2u64(uint64(idx))
					resource.Usage = uint64(idx / 2)
					resource.BackendQuota = p2i64(int64(idx))
					resource.DesiredBackendQuota = p2u64(uint64(idx))
				}
				err = dbm.Insert(&resource)
				if err != nil {
					t.Fatal(err)
				}
			}
		}
	}

	sort.Slice(expectedProjectsJSON, func(i, j int) bool {
		left := expectedProjectsJSON[i]
		right := expectedProjectsJSON[j]
		return left["id"].(string) < right["id"].(string)
	})
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-germany/projects",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONObject{"projects": expectedProjectsJSON},
	}.Check(t, router)
}

func requestOneQuotaChange(structureLevel, serviceType, resourceName string, quota uint64, unit limes.Unit) assert.JSONObject {
	resource := assert.JSONObject{"name": resourceName, "quota": quota}
	if unit != limes.UnitNone {
		resource["unit"] = string(unit)
	}
	return assert.JSONObject{
		structureLevel: assert.JSONObject{
			"services": []assert.JSONObject{
				{
					"type":      serviceType,
					"resources": []assert.JSONObject{resource},
				},
			},
		},
	}
}

func Test_StrictDomainQuotaLimit(t *testing.T) {
	cluster, _, router, _ := setupTest(t, "fixtures/start-data.sql")

	//set up a QD config for shared/things with the default behavior
	qdConfig := &core.QuotaDistributionConfiguration{
		FullResourceNameRx:     regexp.MustCompile(`^shared/things$`),
		Model:                  limesresources.HierarchicalQuotaDistribution,
		StrictDomainQuotaLimit: false,
	}
	cluster.Config.QuotaDistributionConfigs = append(cluster.Config.QuotaDistributionConfigs, qdConfig)

	//NOTE: The relevant parts of start-data.sql look as follows for "shared/things":
	// cluster_capacity      = 246
	// domain_quota[germany] = 30
	// domain_quota[france]  = 0 (not set)

	//with StrictDomainQuotaLimit = false, we can go over the total capacity
	assert.HTTPRequest{
		Method: "PUT",
		Path:   "/v1/domains/uuid-for-france",
		//this single domain quota is below capacity, but the sum of domain quotas is not
		Body:         requestOneQuotaChange("domain", "shared", "things", 240, limes.UnitNone),
		ExpectStatus: 202,
	}.Check(t, router)

	//with StrictDomainQuotaLimit = true, we can always go down (even if we are not getting back into the green area)...
	qdConfig.StrictDomainQuotaLimit = true
	assert.HTTPRequest{
		Method: "PUT",
		Path:   "/v1/domains/uuid-for-france",
		//this single domain quota is below capacity, but the sum of domain quotas is not
		Body:         requestOneQuotaChange("domain", "shared", "things", 220, limes.UnitNone),
		ExpectStatus: 202,
	}.Check(t, router)

	//...and we can also just stay at the same level...
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-france",
		Body:         requestOneQuotaChange("domain", "shared", "things", 220, limes.UnitNone),
		ExpectStatus: 202,
	}.Check(t, router)

	//...but we cannot go higher into the red
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-france",
		Body:         requestOneQuotaChange("domain", "shared", "things", 240, limes.UnitNone),
		ExpectStatus: 409,
		ExpectBody:   assert.StringData("cannot change shared/things quota: cluster capacity may not be exceeded for this resource (maximum acceptable domain quota is 216)\n"),
	}.Check(t, router)

	//we can get exactly to the limit though (where all capacity is given out as domain quota)
	assert.HTTPRequest{
		Method:       "PUT",
		Path:         "/v1/domains/uuid-for-france",
		Body:         requestOneQuotaChange("domain", "shared", "things", 216, limes.UnitNone),
		ExpectStatus: 202,
	}.Check(t, router)
}