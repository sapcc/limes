// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package reports_v2_test

import (
	"testing"

	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/httptest"
	"github.com/sapcc/go-bits/must"
	"go.xyrillian.de/gg/assert"

	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/limes/internal/api/reports_v2"
	"github.com/sapcc/limes/internal/apideclarations/apiv2/common"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/test"
	"github.com/sapcc/limes/internal/test/common_fixtures"
)

var filterConfigJSON = string(must.Return(httptest.NewJQModifiableJSONString("{}", "filterConfigJSON").
	ModifyWithVariable(". * $ref", common_fixtures.AreaLiquidFirstSecond).
	ModifyWithVariable(".availability_zones = $ref", common_fixtures.AZsOneTwo).
	ModifyWithVariable(".discovery = $ref", common_fixtures.DiscoveryBerlinDresdenParis).
	MarshalJSON()))

func TestV2FilterCreation(t *testing.T) {
	srvInfoFirst := test.DefaultLiquidServiceInfo("First")
	srvInfoFirst.Rates = map[liquid.RateName]liquid.RateInfo{
		"objects:create": {Topology: liquid.FlatTopology, HasUsage: false, Category: Some(liquid.CategoryName("foo_category"))},
		"objects:update": {Topology: liquid.FlatTopology, HasUsage: false},
	}

	s := test.NewSetup(t,
		test.WithConfig(filterConfigJSON),
		test.WithPersistedServiceInfo("first", srvInfoFirst),
		test.WithPersistedServiceInfo("second", test.DefaultLiquidServiceInfo("Second")),
	)
	// do some basic assertions to compare the filtered results against
	sis := s.Cluster.SIC.GetSnapshot()
	assert.Equal(t, len(sis.GetServices()), 2)
	assert.Equal(t, len(must.BeOK(sis.GetResourcesForType("first"))), 2)
	assert.Equal(t, len(must.BeOK(sis.GetResourcesForType("second"))), 2)
	assert.Equal(t, len(must.BeOK(sis.GetRatesForType("first"))), 2)
	assert.Equal(t, len(must.NotBeOK(sis.GetRatesForType("second"))), 0)

	// empty opts yields the same service info
	resourceOpts := common.ResourceReportOpts{}
	resourceFilter := must.ReturnT(reports_v2.FilterFromResourceOpts(s.Cluster, resourceOpts))(t)
	assert.Equal(t, len(resourceFilter.GetServices()), 2)
	assert.Equal(t, len(must.BeOK(resourceFilter.GetResourcesForType("first"))), 2)
	assert.Equal(t, len(must.BeOK(resourceFilter.GetResourcesForType("second"))), 2)
	assert.Equal(t, len(must.BeOK(resourceFilter.GetRatesForType("first"))), 2)
	assert.Equal(t, len(must.NotBeOK(resourceFilter.GetRatesForType("second"))), 0)

	rateOpts := common.RateReportOpts{}
	rateFilter := must.ReturnT(reports_v2.FilterFromRateOpts(s.Cluster, rateOpts))(t)
	assert.Equal(t, len(rateFilter.GetServices()), 2)
	assert.Equal(t, len(must.BeOK(rateFilter.GetResourcesForType("first"))), 2)
	assert.Equal(t, len(must.BeOK(rateFilter.GetResourcesForType("second"))), 2)
	assert.Equal(t, len(must.BeOK(rateFilter.GetRatesForType("first"))), 2)
	assert.Equal(t, len(must.NotBeOK(rateFilter.GetRatesForType("second"))), 0)

	// area filter
	resourceOpts = common.ResourceReportOpts{GenericReportOpts: common.GenericReportOpts{Area: Some("second")}}
	resourceFilter = must.ReturnT(reports_v2.FilterFromResourceOpts(s.Cluster, resourceOpts))(t)
	assert.Equal(t, len(resourceFilter.GetServices()), 1)
	assert.Equal(t, len(must.NotBeOK(resourceFilter.GetResourcesForType("first"))), 0)
	assert.Equal(t, len(must.BeOK(resourceFilter.GetResourcesForType("second"))), 2)
	assert.Equal(t, len(must.NotBeOK(resourceFilter.GetRatesForType("first"))), 0)
	assert.Equal(t, len(must.NotBeOK(resourceFilter.GetRatesForType("second"))), 0)

	rateOpts = common.RateReportOpts{GenericReportOpts: common.GenericReportOpts{Area: Some("second")}}
	rateFilter = must.ReturnT(reports_v2.FilterFromRateOpts(s.Cluster, rateOpts))(t)
	assert.Equal(t, len(rateFilter.GetServices()), 1)
	assert.Equal(t, len(must.NotBeOK(rateFilter.GetResourcesForType("first"))), 0)
	assert.Equal(t, len(must.BeOK(rateFilter.GetResourcesForType("second"))), 2)
	assert.Equal(t, len(must.NotBeOK(rateFilter.GetRatesForType("first"))), 0)
	assert.Equal(t, len(must.NotBeOK(rateFilter.GetRatesForType("second"))), 0)

	// service filter
	resourceOpts = common.ResourceReportOpts{GenericReportOpts: common.GenericReportOpts{ServiceType: Some(db.ServiceType("second"))}}
	resourceFilter = must.ReturnT(reports_v2.FilterFromResourceOpts(s.Cluster, resourceOpts))(t)
	assert.Equal(t, len(resourceFilter.GetServices()), 1)
	assert.Equal(t, len(must.NotBeOK(resourceFilter.GetResourcesForType("first"))), 0)
	assert.Equal(t, len(must.BeOK(resourceFilter.GetResourcesForType("second"))), 2)
	assert.Equal(t, len(must.NotBeOK(resourceFilter.GetRatesForType("first"))), 0)
	assert.Equal(t, len(must.NotBeOK(resourceFilter.GetRatesForType("second"))), 0)

	rateOpts = common.RateReportOpts{GenericReportOpts: common.GenericReportOpts{ServiceType: Some(db.ServiceType("second"))}}
	rateFilter = must.ReturnT(reports_v2.FilterFromRateOpts(s.Cluster, rateOpts))(t)
	assert.Equal(t, len(rateFilter.GetServices()), 1)
	assert.Equal(t, len(must.NotBeOK(rateFilter.GetResourcesForType("first"))), 0)
	assert.Equal(t, len(must.BeOK(rateFilter.GetResourcesForType("second"))), 2)
	assert.Equal(t, len(must.NotBeOK(rateFilter.GetRatesForType("first"))), 0)
	assert.Equal(t, len(must.NotBeOK(rateFilter.GetRatesForType("second"))), 0)

	// category filter
	resourceOpts = common.ResourceReportOpts{GenericReportOpts: common.GenericReportOpts{Category: Some(liquid.CategoryName("foo_category"))}}
	resourceFilter = must.ReturnT(reports_v2.FilterFromResourceOpts(s.Cluster, resourceOpts))(t)
	assert.Equal(t, len(resourceFilter.GetServices()), 2)
	assert.Equal(t, len(must.BeOK(resourceFilter.GetResourcesForType("first"))), 1)
	assert.Equal(t, (must.BeOK(resourceFilter.GetResourcesForType("first")))["capacity"].CategoryID.IsSome(), true)
	assert.Equal(t, len(must.BeOK(resourceFilter.GetResourcesForType("second"))), 1)
	assert.Equal(t, len(must.BeOK(resourceFilter.GetRatesForType("first"))), 1)
	assert.Equal(t, (must.BeOK(resourceFilter.GetRatesForType("first")))["objects:create"].CategoryID.IsSome(), true)
	assert.Equal(t, len(must.NotBeOK(resourceFilter.GetRatesForType("second"))), 0)

	rateOpts = common.RateReportOpts{GenericReportOpts: common.GenericReportOpts{Category: Some(liquid.CategoryName("foo_category"))}}
	rateFilter = must.ReturnT(reports_v2.FilterFromRateOpts(s.Cluster, rateOpts))(t)
	assert.Equal(t, len(rateFilter.GetServices()), 2)
	assert.Equal(t, len(must.BeOK(rateFilter.GetResourcesForType("first"))), 1)
	assert.Equal(t, (must.BeOK(resourceFilter.GetResourcesForType("first")))["capacity"].CategoryID.IsSome(), true)
	assert.Equal(t, len(must.BeOK(rateFilter.GetResourcesForType("second"))), 1)
	assert.Equal(t, len(must.BeOK(rateFilter.GetRatesForType("first"))), 1)
	assert.Equal(t, (must.BeOK(resourceFilter.GetRatesForType("first")))["objects:create"].CategoryID.IsSome(), true)
	assert.Equal(t, len(must.NotBeOK(rateFilter.GetRatesForType("second"))), 0)

	// category filter: using serviceType value as category to get resources/rates without explicit category
	resourceOpts = common.ResourceReportOpts{GenericReportOpts: common.GenericReportOpts{Category: Some(liquid.CategoryName("first"))}}
	resourceFilter = must.ReturnT(reports_v2.FilterFromResourceOpts(s.Cluster, resourceOpts))(t)
	assert.Equal(t, len(resourceFilter.GetServices()), 1)
	assert.Equal(t, len(must.BeOK(resourceFilter.GetResourcesForType("first"))), 1)
	assert.Equal(t, (must.BeOK(resourceFilter.GetResourcesForType("first")))["things"].CategoryID.IsSome(), false)
	assert.Equal(t, len(must.NotBeOK(resourceFilter.GetResourcesForType("second"))), 0)
	assert.Equal(t, len(must.BeOK(resourceFilter.GetRatesForType("first"))), 1)
	assert.Equal(t, (must.BeOK(resourceFilter.GetRatesForType("first")))["objects:update"].CategoryID.IsSome(), false)
	assert.Equal(t, len(must.NotBeOK(resourceFilter.GetRatesForType("second"))), 0)

	rateOpts = common.RateReportOpts{GenericReportOpts: common.GenericReportOpts{Category: Some(liquid.CategoryName("first"))}}
	rateFilter = must.ReturnT(reports_v2.FilterFromRateOpts(s.Cluster, rateOpts))(t)
	assert.Equal(t, len(rateFilter.GetServices()), 1)
	assert.Equal(t, len(must.BeOK(rateFilter.GetResourcesForType("first"))), 1)
	assert.Equal(t, (must.BeOK(rateFilter.GetResourcesForType("first")))["things"].CategoryID.IsSome(), false)
	assert.Equal(t, len(must.NotBeOK(rateFilter.GetResourcesForType("second"))), 0)
	assert.Equal(t, len(must.BeOK(rateFilter.GetRatesForType("first"))), 1)
	assert.Equal(t, (must.BeOK(rateFilter.GetRatesForType("first")))["objects:update"].CategoryID.IsSome(), false)
	assert.Equal(t, len(must.NotBeOK(rateFilter.GetRatesForType("second"))), 0)

	// resource filter
	resourceOpts = common.ResourceReportOpts{ResourceName: Some(liquid.ResourceName("capacity"))}
	resourceFilter = must.ReturnT(reports_v2.FilterFromResourceOpts(s.Cluster, resourceOpts))(t)
	assert.Equal(t, len(resourceFilter.GetServices()), 2)
	assert.Equal(t, len(must.BeOK(resourceFilter.GetResourcesForType("first"))), 1)
	assert.Equal(t, len(must.BeOK(resourceFilter.GetResourcesForType("second"))), 1)
	assert.Equal(t, len(must.BeOK(resourceFilter.GetRatesForType("first"))), 2)
	assert.Equal(t, len(must.NotBeOK(resourceFilter.GetRatesForType("second"))), 0)

	// rate filter
	rateOpts = common.RateReportOpts{RateName: Some(liquid.RateName("objects:create"))}
	rateFilter = must.ReturnT(reports_v2.FilterFromRateOpts(s.Cluster, rateOpts))(t)
	assert.Equal(t, len(rateFilter.GetServices()), 2)
	assert.Equal(t, len(must.BeOK(rateFilter.GetResourcesForType("first"))), 2)
	assert.Equal(t, len(must.BeOK(rateFilter.GetResourcesForType("second"))), 2)
	assert.Equal(t, len(must.BeOK(rateFilter.GetRatesForType("first"))), 1)
	assert.Equal(t, len(must.NotBeOK(rateFilter.GetRatesForType("second"))), 0)
}

func TestV2ExpandServiceFilters(t *testing.T) {
	srvInfoFirst := test.DefaultLiquidServiceInfo("First")
	srvInfoFirst.Rates = map[liquid.RateName]liquid.RateInfo{
		"objects:create": {Topology: liquid.FlatTopology, HasUsage: false, Category: Some(liquid.CategoryName("foo_category"))},
		"objects:update": {Topology: liquid.FlatTopology, HasUsage: false},
	}

	s := test.NewSetup(t,
		test.WithConfig(filterConfigJSON),
		test.WithPersistedServiceInfo("first", srvInfoFirst),
		test.WithPersistedServiceInfo("second", test.DefaultLiquidServiceInfo("Second")),
	)

	// unfiltered: all placeholders become TRUE = TRUE
	unfiltered := must.ReturnT(reports_v2.FilterFromResourceOpts(s.Cluster, common.ResourceReportOpts{}))(t)
	query, args := unfiltered.ExpandServiceFilters(
		`SELECT * FROM t WHERE {{s.id = ANY($service_id)}} AND {{r.id = ANY($resource_id)}} AND {{ra.id = ANY($rate_id)}}`,
	)
	assert.Equal(t, query, `SELECT * FROM t WHERE TRUE = TRUE AND TRUE = TRUE AND TRUE = TRUE`)
	assert.Equal(t, len(args), 0)

	// filtered by area: all three get replaced with args
	filtered := must.ReturnT(reports_v2.FilterFromResourceOpts(s.Cluster, common.ResourceReportOpts{
		GenericReportOpts: common.GenericReportOpts{Area: Some("first")},
	}))(t)
	query, args = filtered.ExpandServiceFilters(
		`SELECT * FROM t WHERE {{s.id = ANY($service_id)}} AND {{r.id = ANY($resource_id)}} AND {{ra.id = ANY($rate_id)}}`,
	)
	assert.Equal(t, query, `SELECT * FROM t WHERE s.id = ANY($1) AND r.id = ANY($2) AND ra.id = ANY($3)`)
	assert.Equal(t, len(args), 3)

	// filtered by service type: service_id and resource_id get arg positions
	filtered = must.ReturnT(reports_v2.FilterFromResourceOpts(s.Cluster, common.ResourceReportOpts{
		GenericReportOpts: common.GenericReportOpts{ServiceType: Some(db.ServiceType("first"))},
	}))(t)
	query, args = filtered.ExpandServiceFilters(
		`SELECT * FROM t WHERE {{s.id = ANY($service_id)}} AND {{r.id = ANY($resource_id)}}`,
	)
	assert.Equal(t, query, `SELECT * FROM t WHERE s.id = ANY($1) AND r.id = ANY($2)`)
	assert.Equal(t, len(args), 2)

	// filtered by resource name only: all placeholders get args (filter is non-empty)
	filtered = must.ReturnT(reports_v2.FilterFromResourceOpts(s.Cluster, common.ResourceReportOpts{
		ResourceName: Some(liquid.ResourceName("capacity")),
	}))(t)
	query, args = filtered.ExpandServiceFilters(
		`SELECT * FROM t WHERE {{s.id = ANY($service_id)}} AND {{r.id = ANY($resource_id)}}`,
	)
	assert.Equal(t, query, `SELECT * FROM t WHERE s.id = ANY($1) AND r.id = ANY($2)`)
	assert.Equal(t, len(args), 2)

	// filtered by rate name only: all placeholders get args (filter is non-empty)
	rateFiltered := must.ReturnT(reports_v2.FilterFromRateOpts(s.Cluster, common.RateReportOpts{
		RateName: Some(liquid.RateName("objects:create")),
	}))(t)
	query, args = rateFiltered.ExpandServiceFilters(
		`SELECT * FROM t WHERE {{s.id = ANY($service_id)}} AND {{ra.id = ANY($rate_id)}}`,
	)
	assert.Equal(t, query, `SELECT * FROM t WHERE s.id = ANY($1) AND ra.id = ANY($2)`)
	assert.Equal(t, len(args), 2)

	// with pre-existing args: arg positions continue from highest existing position
	filtered = must.ReturnT(reports_v2.FilterFromResourceOpts(s.Cluster, common.ResourceReportOpts{
		GenericReportOpts: common.GenericReportOpts{ServiceType: Some(db.ServiceType("first"))},
	}))(t)
	query, args = filtered.ExpandServiceFilters(
		`SELECT * FROM t WHERE t.name = $14 AND {{s.id = ANY($service_id)}}`,
		"some-value",
	)
	assert.Equal(t, query, `SELECT * FROM t WHERE t.name = $14 AND s.id = ANY($15)`)
	assert.Equal(t, len(args), 2)
	assert.Equal(t, args[0].(string), "some-value")
}
