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

package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/go-bits/must"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/test"
	"github.com/sapcc/limes/internal/test/plugins"
)

const (
	testSmallConfigYAML = `
		availability_zones: [ az-one, az-two ]
		discovery:
			method: --test-static
		services:
			- service_type: first
				type: --test-generic
	`
)

///////////////////////////////////////////////////////////////////////////////////////////
// subcapacity translation

func TestTranslateManilaSubcapacities(t *testing.T) {
	// this is what liquid-manila (or liquid-cinder) writes into the DB
	subcapacitiesInLiquidFormat := []assert.JSONObject{
		{
			"name":     "pool1",
			"capacity": 520,
			"usage":    520,
			"attributes": assert.JSONObject{
				"exclusion_reason": "hardware_state = in_decom",
				"real_capacity":    15360,
			},
		},
		{
			"name":       "pool2",
			"capacity":   15360,
			"usage":      10,
			"attributes": assert.JSONObject{},
		},
	}

	// this is what we expect to be reported on the API
	subcapacitiesInLegacyFormat := []assert.JSONObject{
		{
			"pool_name":        "pool1",
			"az":               "az-one",
			"capacity_gib":     15360,
			"usage_gib":        520,
			"exclusion_reason": "hardware_state = in_decom",
		},
		{
			"pool_name":        "pool2",
			"az":               "az-one",
			"capacity_gib":     15360,
			"usage_gib":        10,
			"exclusion_reason": "",
		},
	}

	testSubcapacityTranslation(t, "cinder-manila-capacity", nil, subcapacitiesInLiquidFormat, subcapacitiesInLegacyFormat)
}

func TestTranslateIronicSubcapacities(t *testing.T) {
	extraSetup := func(s *test.Setup) {
		// this subcapacity translation depends on ResourceInfo.Attributes on the respective resource
		plugin := s.Cluster.QuotaPlugins["first"].(*plugins.GenericQuotaPlugin)
		plugin.StaticResourceAttributes = map[liquid.ResourceName]map[string]any{"capacity": {
			"cores":    5,
			"ram_mib":  23,
			"disk_gib": 42,
		}}
	}

	subcapacitiesInLiquidFormat := []assert.JSONObject{
		{
			"id":       "c28b2abb-0da6-4b37-81f6-5ae255d53e1f",
			"name":     "node001",
			"capacity": 1,
			"attributes": assert.JSONObject{
				"provision_state": "AVAILABLE",
				"serial_number":   "98105291",
			},
		},
		{
			"id":       "6f8c1838-42db-4d7e-b3c0-98f3bc59fd62",
			"name":     "node002",
			"capacity": 1,
			"attributes": assert.JSONObject{
				"provision_state":        "DEPLOYING",
				"target_provision_state": "ACTIVE",
				"serial_number":          "98105292",
				"instance_id":            "1bb45c1a-e10f-449a-abf6-1ffc6c93e113",
			},
		},
	}

	subcapacitiesInLegacyFormat := []assert.JSONObject{
		{
			"id":                "c28b2abb-0da6-4b37-81f6-5ae255d53e1f",
			"name":              "node001",
			"provision_state":   "AVAILABLE",
			"availability_zone": "az-one",
			"ram":               assert.JSONObject{"value": 23, "unit": "MiB"},
			"disk":              assert.JSONObject{"value": 42, "unit": "GiB"},
			"cores":             5,
			"serial":            "98105291",
		},
		{
			"id":                     "6f8c1838-42db-4d7e-b3c0-98f3bc59fd62",
			"name":                   "node002",
			"provision_state":        "DEPLOYING",
			"target_provision_state": "ACTIVE",
			"availability_zone":      "az-one",
			"ram":                    assert.JSONObject{"value": 23, "unit": "MiB"},
			"disk":                   assert.JSONObject{"value": 42, "unit": "GiB"},
			"cores":                  5,
			"serial":                 "98105292",
			"instance_id":            "1bb45c1a-e10f-449a-abf6-1ffc6c93e113",
		},
	}

	testSubcapacityTranslation(t, "ironic-flavors", extraSetup, subcapacitiesInLiquidFormat, subcapacitiesInLegacyFormat)
}

func testSubcapacityTranslation(t *testing.T, ruleID string, extraSetup func(s *test.Setup), subcapacitiesInLiquidFormat, subcapacitiesInLegacyFormat []assert.JSONObject) {
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-small.sql"),
		test.WithConfig(testSmallConfigYAML),
		test.WithAPIHandler(NewV1API),
	)
	s.Cluster.Config.ResourceBehaviors = []core.ResourceBehavior{{
		FullResourceNameRx:     "first/capacity",
		TranslationRuleInV1API: must.Return(core.NewTranslationRule(ruleID)),
	}}

	if extraSetup != nil {
		extraSetup(&s)
	}

	// this is what liquid-manila (or liquid-cinder) writes into the DB
	_, err := s.DB.Exec(`UPDATE cluster_az_resources SET subcapacities = $1 WHERE id = 2`,
		string(must.Return(json.Marshal(subcapacitiesInLiquidFormat))),
	)
	if err != nil {
		t.Fatal(err.Error())
	}

	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/clusters/current?resource=capacity&detail",
		Header:       map[string]string{"X-Limes-V2-API-Preview": "per-az"},
		ExpectStatus: http.StatusOK,
		ExpectBody: assert.JSONObject{
			"cluster": assert.JSONObject{
				"id":             "current",
				"min_scraped_at": 1000,
				"max_scraped_at": 1000,
				"services": []assert.JSONObject{{
					"type":           "first",
					"area":           "first",
					"min_scraped_at": 11,
					"max_scraped_at": 11,
					"resources": []assert.JSONObject{{
						"name":          "capacity",
						"unit":          "B",
						"capacity":      0,
						"domains_quota": 0,
						"usage":         0,
						"per_availability_zone": []assert.JSONObject{
							{"capacity": 0, "name": "az-one"},
							{"capacity": 0, "name": "az-two"},
						},
						"per_az": assert.JSONObject{
							"az-one": assert.JSONObject{"capacity": 0, "usage": 0, "subcapacities": subcapacitiesInLegacyFormat},
							"az-two": assert.JSONObject{"capacity": 0, "usage": 0},
						},
						"quota_distribution_model": "autogrow",
						"subcapacities":            subcapacitiesInLegacyFormat,
					}},
				}},
			},
		},
	}.Check(t, s.Handler)
}

///////////////////////////////////////////////////////////////////////////////////////////
// subresource translation

func TestTranslateCinderVolumeSubresources(t *testing.T) {
	subresourcesInLiquidFormat := []assert.JSONObject{
		{
			"id":   "6dfbbce3-078d-4c64-8f88-8145b8d44183",
			"name": "volume1",
			"attributes": assert.JSONObject{
				"size_gib": 21,
				"status":   "error",
			},
		},
		{
			"id":   "a33bae62-e14e-47a6-a019-5faf89c20dc7",
			"name": "volume2",
			"attributes": assert.JSONObject{
				"size_gib": 1,
				"status":   "available",
			},
		},
	}

	subresourcesInLegacyFormat := []assert.JSONObject{
		{
			"id":                "6dfbbce3-078d-4c64-8f88-8145b8d44183",
			"name":              "volume1",
			"status":            "error",
			"size":              assert.JSONObject{"value": 21, "unit": "GiB"},
			"availability_zone": "az-one",
		},
		{
			"id":                "a33bae62-e14e-47a6-a019-5faf89c20dc7",
			"name":              "volume2",
			"status":            "available",
			"size":              assert.JSONObject{"value": 1, "unit": "GiB"},
			"availability_zone": "az-one",
		},
	}

	testSubresourceTranslation(t, "cinder-volumes", nil, subresourcesInLiquidFormat, subresourcesInLegacyFormat)
}

func TestTranslateCinderSnapshotSubresources(t *testing.T) {
	subresourcesInLiquidFormat := []assert.JSONObject{
		{
			"id":   "260da0ee-4816-48af-8784-1717cb76c0cd",
			"name": "snapshot1-of-volume2",
			"attributes": assert.JSONObject{
				"size_gib":  1,
				"status":    "available",
				"volume_id": "a33bae62-e14e-47a6-a019-5faf89c20dc7",
			},
		},
	}

	subresourcesInLegacyFormat := []assert.JSONObject{
		{
			"id":        "260da0ee-4816-48af-8784-1717cb76c0cd",
			"name":      "snapshot1-of-volume2",
			"status":    "available",
			"size":      assert.JSONObject{"value": 1, "unit": "GiB"},
			"volume_id": "a33bae62-e14e-47a6-a019-5faf89c20dc7",
		},
	}

	testSubresourceTranslation(t, "cinder-snapshots", nil, subresourcesInLiquidFormat, subresourcesInLegacyFormat)
}

func TestTranslateIronicSubresources(t *testing.T) {
	extraSetup := func(s *test.Setup) {
		// this subcapacity translation depends on ResourceInfo.Attributes on the respective resource
		plugin := s.Cluster.QuotaPlugins["first"].(*plugins.GenericQuotaPlugin)
		plugin.StaticResourceAttributes = map[liquid.ResourceName]map[string]any{"capacity": {
			"cores":    5,
			"ram_mib":  23,
			"disk_gib": 42,
		}}
	}

	subresourcesInLiquidFormat := []assert.JSONObject{
		{
			"id":   "7c84fbdb-9a18-43b4-be3e-d45c267d821b",
			"name": "minimal-instance",
			"attributes": assert.JSONObject{
				"status":  "ACTIVE",
				"os_type": "rhel9",
			},
		},
		{
			"id":   "248bbfcc-e2cd-4ccc-9782-f2a8050da612",
			"name": "maximal-instance",
			"attributes": assert.JSONObject{
				"status":   "ACTIVE",
				"metadata": assert.JSONObject{"foo": "bar"},
				"tags":     []string{"foobar"},
				"os_type":  "image-deleted",
			},
		},
	}

	subresourcesInLegacyFormat := []assert.JSONObject{
		{
			"id":                "7c84fbdb-9a18-43b4-be3e-d45c267d821b",
			"name":              "minimal-instance",
			"status":            "ACTIVE",
			"metadata":          nil,
			"tags":              nil,
			"availability_zone": "az-one",
			"hypervisor":        "none",
			"flavor":            "capacity", // this is derived from the resource name, so it looks weird in this test
			"vcpu":              5,
			"ram":               assert.JSONObject{"value": 23, "unit": "MiB"},
			"disk":              assert.JSONObject{"value": 42, "unit": "GiB"},
			"os_type":           "rhel9",
		},
		{
			"id":                "248bbfcc-e2cd-4ccc-9782-f2a8050da612",
			"name":              "maximal-instance",
			"status":            "ACTIVE",
			"metadata":          assert.JSONObject{"foo": "bar"},
			"tags":              []string{"foobar"},
			"availability_zone": "az-one",
			"hypervisor":        "none",
			"flavor":            "capacity",
			"vcpu":              5,
			"ram":               assert.JSONObject{"value": 23, "unit": "MiB"},
			"disk":              assert.JSONObject{"value": 42, "unit": "GiB"},
			"os_type":           "image-deleted",
		},
	}

	testSubresourceTranslation(t, "ironic-flavors", extraSetup, subresourcesInLiquidFormat, subresourcesInLegacyFormat)
}

func testSubresourceTranslation(t *testing.T, ruleID string, extraSetup func(s *test.Setup), subresourcesInLiquidFormat, subresourcesInLegacyFormat []assert.JSONObject) {
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-small.sql"),
		test.WithConfig(testSmallConfigYAML),
		test.WithAPIHandler(NewV1API),
	)
	s.Cluster.Config.ResourceBehaviors = []core.ResourceBehavior{{
		FullResourceNameRx:     "first/capacity",
		TranslationRuleInV1API: must.Return(core.NewTranslationRule(ruleID)),
	}}

	if extraSetup != nil {
		extraSetup(&s)
	}

	_, err := s.DB.Exec(`UPDATE project_az_resources SET subresources = $1 WHERE id = 3`,
		string(must.Return(json.Marshal(subresourcesInLiquidFormat))),
	)
	if err != nil {
		t.Fatal(err.Error())
	}

	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/domains/uuid-for-domainone/projects/uuid-for-projectone?resource=capacity&detail",
		Header:       map[string]string{"X-Limes-V2-API-Preview": "per-az"},
		ExpectStatus: http.StatusOK,
		ExpectBody: assert.JSONObject{
			"project": assert.JSONObject{
				"id":        "uuid-for-projectone",
				"name":      "projectone",
				"parent_id": "uuid-for-domainone",
				"services": []assert.JSONObject{
					{
						"type":       "first",
						"area":       "first",
						"scraped_at": 11,
						"resources": []assert.JSONObject{
							{
								"name":         "capacity",
								"unit":         "B",
								"quota":        0,
								"usable_quota": 0,
								"usage":        0,
								"per_az": assert.JSONObject{
									"az-one": assert.JSONObject{"quota": 0, "usage": 0, "subresources": subresourcesInLegacyFormat},
									"az-two": assert.JSONObject{"quota": 0, "usage": 0},
								},
								"quota_distribution_model": "autogrow",
								"subresources":             subresourcesInLegacyFormat,
							},
						},
					},
				},
			},
		},
	}.Check(t, s.Handler)
}
