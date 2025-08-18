// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package datamodel

import (
	"testing"

	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/go-bits/easypg"

	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/test"
)

func TestMain(m *testing.M) {
	easypg.WithTestDB(m, func() int { return m.Run() })
}

const (
	testQuotaOverridesNoRenamingConfigYAML = `
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
			first:
				area: first
			second:
				area: second
	`

	testQuotaOverridesWithRenamingConfigYAML = `
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
			first:
				area: first
			second:
				area: second
		resource_behavior:
		- resource: first/capacity
			identity_in_v1_api: capacities/first
		- resource: (first)/thi(ngs)
			identity_in_v1_api: thi$2/$1
	`
)

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
	srvInfo := test.DefaultLiquidServiceInfo()
	test.NewMockLiquidClient(srvInfo, "first")
	test.NewMockLiquidClient(srvInfo, "second")
	s := test.NewSetup(t,
		test.WithConfig(testQuotaOverridesNoRenamingConfigYAML),
		// here, we use the LiquidConnections, as this runs within the collect task
		test.WithLiquidConnections,
	)
	overrides, errs := LoadQuotaOverrides(s.Cluster)
	for _, err := range errs {
		t.Error(err.Error())
	}
	assert.DeepEqual(t, "quota overrides", overrides, expectedQuotaOverrides)
}

func TestQuotaOverridesWithResourceRenaming(t *testing.T) {
	t.Setenv("LIMES_QUOTA_OVERRIDES_PATH", "fixtures/quota-overrides-with-renaming.json")
	srvInfo := test.DefaultLiquidServiceInfo()
	test.NewMockLiquidClient(srvInfo, "first")
	test.NewMockLiquidClient(srvInfo, "second")
	s := test.NewSetup(t,
		test.WithConfig(testQuotaOverridesWithRenamingConfigYAML),
		// here, we use the LiquidConnections, as this runs within the collect task
		test.WithLiquidConnections,
	)
	overrides, errs := LoadQuotaOverrides(s.Cluster)
	for _, err := range errs {
		t.Error(err.Error())
	}
	assert.DeepEqual(t, "quota overrides", overrides, expectedQuotaOverrides)
}
