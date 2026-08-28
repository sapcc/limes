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
	assert.Equal(t, sis.GetServices().Len(), 2)
	assert.Equal(t, sis.GetResourcesForType("first").Len(), 2)
	assert.Equal(t, sis.GetResourcesForType("second").Len(), 2)
	assert.Equal(t, sis.GetRatesForType("first").Len(), 2)
	assert.Equal(t, sis.GetRatesForType("second").Len(), 0)

	// empty opts yields the same service info
	resourceOpts := common.ResourceReportOpts{}
	resourceFilter := must.ReturnT(reports_v2.FilterFromResourceOpts(s.Cluster, resourceOpts))(t)
	assert.Equal(t, resourceFilter.GetServices().Len(), 2)
	assert.Equal(t, resourceFilter.GetResourcesForType("first").Len(), 2)
	assert.Equal(t, resourceFilter.GetResourcesForType("second").Len(), 2)
	assert.Equal(t, resourceFilter.GetRatesForType("first").Len(), 2)
	assert.Equal(t, resourceFilter.GetRatesForType("second").Len(), 0)

	rateOpts := common.RateReportOpts{}
	rateFilter := must.ReturnT(reports_v2.FilterFromRateOpts(s.Cluster, rateOpts))(t)
	assert.Equal(t, rateFilter.GetServices().Len(), 2)
	assert.Equal(t, rateFilter.GetResourcesForType("first").Len(), 2)
	assert.Equal(t, rateFilter.GetResourcesForType("second").Len(), 2)
	assert.Equal(t, rateFilter.GetRatesForType("first").Len(), 2)
	assert.Equal(t, rateFilter.GetRatesForType("second").Len(), 0)

	// area filter
	resourceOpts = common.ResourceReportOpts{Area: Some("second")}
	resourceFilter = must.ReturnT(reports_v2.FilterFromResourceOpts(s.Cluster, resourceOpts))(t)
	assert.Equal(t, resourceFilter.GetServices().Len(), 1)
	assert.Equal(t, resourceFilter.GetResourcesForType("first").Len(), 0)
	assert.Equal(t, resourceFilter.GetResourcesForType("second").Len(), 2)
	assert.Equal(t, resourceFilter.GetRatesForType("first").Len(), 0)
	assert.Equal(t, resourceFilter.GetRatesForType("second").Len(), 0)

	rateOpts = common.RateReportOpts{Area: Some("second")}
	rateFilter = must.ReturnT(reports_v2.FilterFromRateOpts(s.Cluster, rateOpts))(t)
	assert.Equal(t, rateFilter.GetServices().Len(), 1)
	assert.Equal(t, rateFilter.GetResourcesForType("first").Len(), 0)
	assert.Equal(t, rateFilter.GetResourcesForType("second").Len(), 2)
	assert.Equal(t, rateFilter.GetRatesForType("first").Len(), 0)
	assert.Equal(t, rateFilter.GetRatesForType("second").Len(), 0)

	// service filter
	resourceOpts = common.ResourceReportOpts{ServiceType: Some(db.ServiceType("second"))}
	resourceFilter = must.ReturnT(reports_v2.FilterFromResourceOpts(s.Cluster, resourceOpts))(t)
	assert.Equal(t, resourceFilter.GetServices().Len(), 1)
	assert.Equal(t, resourceFilter.GetResourcesForType("first").Len(), 0)
	assert.Equal(t, resourceFilter.GetResourcesForType("second").Len(), 2)
	assert.Equal(t, resourceFilter.GetRatesForType("first").Len(), 0)
	assert.Equal(t, resourceFilter.GetRatesForType("second").Len(), 0)

	rateOpts = common.RateReportOpts{ServiceType: Some(db.ServiceType("second"))}
	rateFilter = must.ReturnT(reports_v2.FilterFromRateOpts(s.Cluster, rateOpts))(t)
	assert.Equal(t, rateFilter.GetServices().Len(), 1)
	assert.Equal(t, rateFilter.GetResourcesForType("first").Len(), 0)
	assert.Equal(t, rateFilter.GetResourcesForType("second").Len(), 2)
	assert.Equal(t, rateFilter.GetRatesForType("first").Len(), 0)
	assert.Equal(t, rateFilter.GetRatesForType("second").Len(), 0)

	// category filter
	resourceOpts = common.ResourceReportOpts{Category: Some(liquid.CategoryName("foo_category"))}
	resourceFilter = must.ReturnT(reports_v2.FilterFromResourceOpts(s.Cluster, resourceOpts))(t)
	assert.Equal(t, resourceFilter.GetServices().Len(), 2)
	assert.Equal(t, resourceFilter.GetResourcesForType("first").Len(), 1)
	assert.Equal(t, resourceFilter.GetResourcesForType("second").Len(), 1)
	assert.Equal(t, resourceFilter.GetRatesForType("first").Len(), 1)
	assert.Equal(t, resourceFilter.GetRatesForType("second").Len(), 0)

	rateOpts = common.RateReportOpts{Category: Some(liquid.CategoryName("foo_category"))}
	rateFilter = must.ReturnT(reports_v2.FilterFromRateOpts(s.Cluster, rateOpts))(t)
	assert.Equal(t, rateFilter.GetServices().Len(), 2)
	assert.Equal(t, rateFilter.GetResourcesForType("first").Len(), 1)
	assert.Equal(t, rateFilter.GetResourcesForType("second").Len(), 1)
	assert.Equal(t, rateFilter.GetRatesForType("first").Len(), 1)
	assert.Equal(t, rateFilter.GetRatesForType("second").Len(), 0)

	// category filter: using serviceType value as category to get resources/rates without explicit category
	resourceOpts = common.ResourceReportOpts{Category: Some(liquid.CategoryName("first"))}
	resourceFilter = must.ReturnT(reports_v2.FilterFromResourceOpts(s.Cluster, resourceOpts))(t)
	assert.Equal(t, resourceFilter.GetServices().Len(), 1)
	assert.Equal(t, resourceFilter.GetResourcesForType("first").Len(), 1)
	assert.Equal(t, resourceFilter.GetResourcesForType("second").Len(), 0)
	assert.Equal(t, resourceFilter.GetRatesForType("first").Len(), 1)
	assert.Equal(t, resourceFilter.GetRatesForType("second").Len(), 0)

	rateOpts = common.RateReportOpts{Category: Some(liquid.CategoryName("first"))}
	rateFilter = must.ReturnT(reports_v2.FilterFromRateOpts(s.Cluster, rateOpts))(t)
	assert.Equal(t, rateFilter.GetServices().Len(), 1)
	assert.Equal(t, rateFilter.GetResourcesForType("first").Len(), 1)
	assert.Equal(t, rateFilter.GetResourcesForType("second").Len(), 0)
	assert.Equal(t, rateFilter.GetRatesForType("first").Len(), 1)
	assert.Equal(t, rateFilter.GetRatesForType("second").Len(), 0)

	// resource filter
	resourceOpts = common.ResourceReportOpts{ResourceName: Some(liquid.ResourceName("capacity"))}
	resourceFilter = must.ReturnT(reports_v2.FilterFromResourceOpts(s.Cluster, resourceOpts))(t)
	assert.Equal(t, resourceFilter.GetServices().Len(), 2)
	assert.Equal(t, resourceFilter.GetResourcesForType("first").Len(), 1)
	assert.Equal(t, resourceFilter.GetResourcesForType("second").Len(), 1)
	assert.Equal(t, resourceFilter.GetRatesForType("first").Len(), 2)
	assert.Equal(t, resourceFilter.GetRatesForType("second").Len(), 0)

	// rate filter
	rateOpts = common.RateReportOpts{RateName: Some(liquid.RateName("objects:create"))}
	rateFilter = must.ReturnT(reports_v2.FilterFromRateOpts(s.Cluster, rateOpts))(t)
	assert.Equal(t, rateFilter.GetServices().Len(), 2)
	assert.Equal(t, rateFilter.GetResourcesForType("first").Len(), 2)
	assert.Equal(t, rateFilter.GetResourcesForType("second").Len(), 2)
	assert.Equal(t, rateFilter.GetRatesForType("first").Len(), 1)
	assert.Equal(t, rateFilter.GetRatesForType("second").Len(), 0)
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
		Area: Some("first"),
	}))(t)
	query, args = filtered.ExpandServiceFilters(
		`SELECT * FROM t WHERE {{s.id = ANY($service_id)}} AND {{r.id = ANY($resource_id)}} AND {{ra.id = ANY($rate_id)}}`,
	)
	assert.Equal(t, query, `SELECT * FROM t WHERE s.id = ANY($1) AND r.id = ANY($2) AND ra.id = ANY($3)`)
	assert.Equal(t, len(args), 3)

	// filtered by service type: service_id and resource_id get arg positions
	filtered = must.ReturnT(reports_v2.FilterFromResourceOpts(s.Cluster, common.ResourceReportOpts{
		ServiceType: Some(db.ServiceType("first")),
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
		ServiceType: Some(db.ServiceType("first")),
	}))(t)
	query, args = filtered.ExpandServiceFilters(
		`SELECT * FROM t WHERE t.name = $14 AND {{s.id = ANY($service_id)}}`,
		"some-value",
	)
	assert.Equal(t, query, `SELECT * FROM t WHERE t.name = $14 AND s.id = ANY($15)`)
	assert.Equal(t, len(args), 2)
	assert.Equal(t, args[0].(string), "some-value")
}
