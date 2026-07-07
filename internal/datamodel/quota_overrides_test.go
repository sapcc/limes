// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package datamodel_test

import (
	"testing"

	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/httptest"
	"github.com/sapcc/go-bits/must"
	"go.xyrillian.de/gg/assert"

	"github.com/sapcc/limes/internal/datamodel"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/test"
	"github.com/sapcc/limes/internal/test/common_fixtures"
)

func TestMain(m *testing.M) {
	easypg.WithTestDB(m, func() int { return m.Run() })
}

var testQuotaOverridesNoRenamingConfigJSON = string(must.Return(httptest.NewJQModifiableJSONString("{}", "testQuotaOverridesNoRenamingConfigJSON").
	ModifyWithVariable(".discovery = $ref", common_fixtures.DiscoveryBerlinDresdenParis).
	ModifyWithVariable(". * $ref", common_fixtures.AreaLiquidFirstSecond).
	ModifyWithVariable(".availability_zones = $ref", common_fixtures.AZsOneTwo).
	MarshalJSON()))

var testQuotaOverridesWithRenamingConfigJSON = string(must.Return(httptest.NewJQModifiableJSONString(`
	{
		"resource_behavior": [
			{"resource": "first/capacity", "identity_in_v1_api": "capacities/first"},
			{"resource": "(first)/thi(ngs)", "identity_in_v1_api": "thi$2/$1"}
		]
	}`, "testQuotaOverridesWithRenamingConfigJSON").
	ModifyWithVariable(".discovery = $ref", common_fixtures.DiscoveryBerlinDresdenParis).
	ModifyWithVariable(". * $ref", common_fixtures.AreaLiquidFirstSecond).
	ModifyWithVariable(".availability_zones = $ref", common_fixtures.AZsOneTwo).
	MarshalJSON()))

var expectedQuotaOverrides = map[string]map[string]map[db.ServiceType]map[liquid.ResourceName]uint64{
	"firstdomain": {
		"firstproject": {
			"first": {
				"capacity": 10,
				"things":   20,
			},
			"second": {
				"capacity": 30,
				"things":   40,
			},
		},
		"secondproject": {
			"first": {
				"capacity": 50,
			},
			"second": {
				"capacity": 60,
			},
		},
	},
	"seconddomain": {
		"thirdproject": {
			"first": {
				"things": 70,
			},
			"second": {
				"things": 80,
			},
		},
	},
}

func TestQuotaOverridesWithoutResourceRenaming(t *testing.T) {
	t.Setenv("LIMES_QUOTA_OVERRIDES_PATH", "fixtures/quota-overrides-no-renaming.json")
	s := test.NewSetup(t,
		test.WithConfig(testQuotaOverridesNoRenamingConfigJSON),
		test.WithMockLiquidClient("first", test.DefaultLiquidServiceInfo("First")),
		test.WithMockLiquidClient("second", test.DefaultLiquidServiceInfo("Second")),
		// here, we use the LiquidConnections, as this runs within the collect task
		test.WithLiquidConnections,
	)
	overrides, errs := datamodel.LoadQuotaOverrides(s.Cluster)
	assert.Equal(t, errs, nil)
	assert.Equal(t, overrides, expectedQuotaOverrides)
}

func TestQuotaOverridesWithResourceRenaming(t *testing.T) {
	t.Setenv("LIMES_QUOTA_OVERRIDES_PATH", "fixtures/quota-overrides-with-renaming.json")
	s := test.NewSetup(t,
		test.WithConfig(testQuotaOverridesWithRenamingConfigJSON),
		test.WithMockLiquidClient("first", test.DefaultLiquidServiceInfo("First")),
		test.WithMockLiquidClient("second", test.DefaultLiquidServiceInfo("Second")),
		// here, we use the LiquidConnections, as this runs within the collect task
		test.WithLiquidConnections,
	)
	overrides, errs := datamodel.LoadQuotaOverrides(s.Cluster)
	assert.Equal(t, errs, nil)
	assert.Equal(t, overrides, expectedQuotaOverrides)
}
