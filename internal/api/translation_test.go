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

	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/go-bits/must"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/test"
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

func TestTranslateManilaSubcapacities(t *testing.T) {
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-small.sql"),
		test.WithConfig(testSmallConfigYAML),
		test.WithAPIHandler(NewV1API),
	)
	s.Cluster.Config.ResourceBehaviors = []core.ResourceBehavior{{
		FullResourceNameRx:     "first/capacity",
		TranslationRuleInV1API: must.Return(core.NewTranslationRule("cinder-manila-capacity")),
	}}

	// this is what liquid-manila (or liquid-cinder) writes into the DB
	newFormatSubcapacities := []assert.JSONObject{
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
	_, err := s.DB.Exec(`UPDATE cluster_az_resources SET subcapacities = $1 WHERE id = 2`,
		string(must.Return(json.Marshal(newFormatSubcapacities))),
	)
	if err != nil {
		t.Fatal(err.Error())
	}

	// this is what we expect to be reported on the API
	oldFormatSubcapacities := []assert.JSONObject{
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
							"az-one": assert.JSONObject{"capacity": 0, "usage": 0, "subcapacities": oldFormatSubcapacities},
							"az-two": assert.JSONObject{"capacity": 0, "usage": 0},
						},
						"quota_distribution_model": "autogrow",
						"subcapacities":            oldFormatSubcapacities,
					}},
				}},
			},
		},
	}.Check(t, s.Handler)
}

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

	testSubresourceTranslation(t, "cinder-volumes", subresourcesInLiquidFormat, subresourcesInLegacyFormat)
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

	testSubresourceTranslation(t, "cinder-snapshots", subresourcesInLiquidFormat, subresourcesInLegacyFormat)
}

func testSubresourceTranslation(t *testing.T, ruleID string, subresourcesInLiquidFormat, subresourcesInLegacyFormat []assert.JSONObject) {
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-small.sql"),
		test.WithConfig(testSmallConfigYAML),
		test.WithAPIHandler(NewV1API),
	)
	s.Cluster.Config.ResourceBehaviors = []core.ResourceBehavior{{
		FullResourceNameRx:     "first/capacity",
		TranslationRuleInV1API: must.Return(core.NewTranslationRule(ruleID)),
	}}

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
