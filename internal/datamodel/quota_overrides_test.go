/*******************************************************************************
*
* Copyright 2024 SAP SE
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

package datamodel

import (
	"testing"

	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/assert"

	"github.com/sapcc/limes/internal/test"
)

const (
	testQuotaOverridesNoRenamingConfigYAML = `
		availability_zones: [ az-one, az-two ]
		discovery:
			method: --test-static
		services:
			- service_type: first
				type: --test-generic
			- service_type: second
				type: --test-generic
	`

	testQuotaOverridesWithRenamingConfigYAML = `
		availability_zones: [ az-one, az-two ]
		discovery:
			method: --test-static
		services:
			- service_type: first
				type: --test-generic
			- service_type: second
				type: --test-generic
		resource_behavior:
		- resource: first/capacity
			identity_in_v1_api: capacities/first
		- resource: first/things
			identity_in_v1_api: things/first
	`
)

var expectedQuotaOverrides = map[string]map[string]map[limes.ServiceType]map[limesresources.ResourceName]uint64{
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
		test.WithConfig(testQuotaOverridesNoRenamingConfigYAML),
	)
	overrides, errs := LoadQuotaOverrides(s.Cluster)
	for _, err := range errs {
		t.Error(err.Error())
	}
	assert.DeepEqual(t, "quota overrides", overrides, expectedQuotaOverrides)
}

func TestQuotaOverridesWithResourceRenaming(t *testing.T) {
	t.Setenv("LIMES_QUOTA_OVERRIDES_PATH", "fixtures/quota-overrides-with-renaming.json")
	s := test.NewSetup(t,
		test.WithConfig(testQuotaOverridesWithRenamingConfigYAML),
	)
	overrides, errs := LoadQuotaOverrides(s.Cluster)
	for _, err := range errs {
		t.Error(err.Error())
	}
	assert.DeepEqual(t, "quota overrides", overrides, expectedQuotaOverrides)
}
