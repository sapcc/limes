// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/go-bits/must"

	"github.com/sapcc/limes/internal/api"
	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/test"
)

const (
	testSmallConfigYAML = `
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

	testSubcapacityTranslation(t, "cinder-manila-capacity", subcapacitiesInLiquidFormat, subcapacitiesInLegacyFormat)
}

func TestTranslateIronicSubcapacities(t *testing.T) {
	// this subcapacity translation depends on ResourceInfo.Attributes on the respective resource
	attrs := map[string]any{
		"cores":    5,
		"ram_mib":  23,
		"disk_gib": 42,
	}
	buf := must.Return(json.Marshal(attrs))
	srvInfo := test.DefaultLiquidServiceInfo()
	resInfo := srvInfo.Resources["capacity"]
	resInfo.Attributes = json.RawMessage(buf)
	srvInfo.Resources["capacity"] = resInfo

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

	testSubcapacityTranslation(t, "ironic-flavors", subcapacitiesInLiquidFormat, subcapacitiesInLegacyFormat, test.WithPersistedServiceInfo("first", srvInfo))
}

func TestTranslateNovaSubcapacities(t *testing.T) {
	subcapacitiesInLiquidFormat := []assert.JSONObject{
		{
			"name":     "nova-compute-bb91",
			"capacity": 448,
			"usage":    1101,
			"attributes": assert.JSONObject{
				"aggregate_name": "vc-a-0",
				"traits":         []string{"COMPUTE_IMAGE_TYPE_ISO", "COMPUTE_IMAGE_TYPE_VMDK", "COMPUTE_NET_ATTACH_INTERFACE", "COMPUTE_NODE", "COMPUTE_RESCUE_BFV", "COMPUTE_SAME_HOST_COLD_MIGRATE", "CUSTOM_BIGVM_DISABLED"},
			},
		},
		{
			"name":     "nova-compute-bb274",
			"capacity": 104,
			"usage":    315,
			"attributes": assert.JSONObject{
				"aggregate_name": "vc-a-1",
				"traits":         []string{"COMPUTE_IMAGE_TYPE_ISO", "COMPUTE_IMAGE_TYPE_VMDK", "COMPUTE_NET_ATTACH_INTERFACE", "COMPUTE_NODE", "COMPUTE_RESCUE_BFV", "COMPUTE_SAME_HOST_COLD_MIGRATE"},
			},
		},
	}

	subcapacitiesInLegacyFormat := []assert.JSONObject{
		{
			"service_host": "nova-compute-bb91",
			"az":           "az-one",
			"aggregate":    "vc-a-0",
			"capacity":     448,
			"usage":        1101,
			"traits":       []string{"COMPUTE_IMAGE_TYPE_ISO", "COMPUTE_IMAGE_TYPE_VMDK", "COMPUTE_NET_ATTACH_INTERFACE", "COMPUTE_NODE", "COMPUTE_RESCUE_BFV", "COMPUTE_SAME_HOST_COLD_MIGRATE", "CUSTOM_BIGVM_DISABLED"},
		},
		{
			"service_host": "nova-compute-bb274",
			"az":           "az-one",
			"aggregate":    "vc-a-1",
			"capacity":     104,
			"usage":        315,
			"traits":       []string{"COMPUTE_IMAGE_TYPE_ISO", "COMPUTE_IMAGE_TYPE_VMDK", "COMPUTE_NET_ATTACH_INTERFACE", "COMPUTE_NODE", "COMPUTE_RESCUE_BFV", "COMPUTE_SAME_HOST_COLD_MIGRATE"},
		},
	}

	testSubcapacityTranslation(t, "nova-flavors", subcapacitiesInLiquidFormat, subcapacitiesInLegacyFormat)
}

func testSubcapacityTranslation(t *testing.T, ruleID string, subcapacitiesInLiquidFormat, subcapacitiesInLegacyFormat []assert.JSONObject, opts ...test.SetupOption) {
	opts = append([]test.SetupOption{
		test.WithDBFixtureFile("fixtures/start-data-small.sql"),
		test.WithConfig(testSmallConfigYAML),
		test.WithAPIHandler(api.NewV1API),
		test.WithMockLiquidClient("first", test.DefaultLiquidServiceInfo()),
	}, opts...)
	s := test.NewSetup(t,
		opts...,
	)
	s.Cluster.Config.ResourceBehaviors = []core.ResourceBehavior{{
		FullResourceNameRx:     "first/capacity",
		TranslationRuleInV1API: must.Return(core.NewTranslationRule(ruleID)),
	}}

	// this is what liquid-manila (or liquid-cinder) writes into the DB
	_, err := s.DB.Exec(`UPDATE az_resources SET subcapacities = $1 WHERE path = $2`,
		string(must.Return(json.Marshal(subcapacitiesInLiquidFormat))),
		"first/capacity/az-one",
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

func TestTranslateIronicSubresources(t *testing.T) {
	// this subcapacity translation depends on ResourceInfo.Attributes on the respective resource
	attrs := map[string]any{
		"cores":    5,
		"ram_mib":  23,
		"disk_gib": 42,
	}
	buf := must.Return(json.Marshal(attrs))
	srvInfo := test.DefaultLiquidServiceInfo()
	resInfo := srvInfo.Resources["capacity"]
	resInfo.Attributes = buf
	srvInfo.Resources["capacity"] = resInfo

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

	testSubresourceTranslation(t, "ironic-flavors", subresourcesInLiquidFormat, subresourcesInLegacyFormat, test.WithPersistedServiceInfo("first", srvInfo))
}

func TestTranslateNovaSubresources(t *testing.T) {
	subresourcesInLiquidFormat := []assert.JSONObject{
		{
			"id":   "c655dfeb-18fa-479d-b0bf-36cd63c2e901",
			"name": "d042639-test-server",
			"attributes": assert.JSONObject{
				"status": "ACTIVE",
				"metadata": assert.JSONObject{
					"image_buildnumber": "",
					"image_name":        "SAP-compliant-ubuntu-24-04",
				},
				"tags":              []string{},
				"availability_zone": "az-one",
				"flavor": assert.JSONObject{
					"name":          "g_c1_m2_v2",
					"vcpu":          1,
					"ram_mib":       2032,
					"disk_gib":      64,
					"video_ram_mib": 16,
				},
				"os_type": "image-deleted",
			},
		},
		{
			"id":   "7cd0f695-75b5-4514-82a2-953237e4c7d6",
			"name": "nsxt-e2e-test-vm-1",
			"attributes": assert.JSONObject{
				"status":            "ACTIVE",
				"metadata":          assert.JSONObject{},
				"tags":              []string{},
				"availability_zone": "az-one",
				"flavor": assert.JSONObject{
					"name":          "g_c8_m16",
					"vcpu":          8,
					"ram_mib":       16368,
					"disk_gib":      64,
					"video_ram_mib": 16,
				},
				"os_type": "image-deleted",
			},
		},
	}

	subresourcesInLegacyFormat := []assert.JSONObject{
		{
			"id":     "c655dfeb-18fa-479d-b0bf-36cd63c2e901",
			"name":   "d042639-test-server",
			"status": "ACTIVE",
			"metadata": assert.JSONObject{
				"image_buildnumber": "",
				"image_name":        "SAP-compliant-ubuntu-24-04",
			},
			"tags":              []string{},
			"availability_zone": "az-one",
			"hypervisor":        "vmware",
			"flavor":            "g_c1_m2_v2",
			"vcpu":              1,
			"ram": assert.JSONObject{
				"value": 2032,
				"unit":  "MiB",
			},
			"disk": assert.JSONObject{
				"value": 64,
				"unit":  "GiB",
			},
			"video_ram": assert.JSONObject{
				"value": 16,
				"unit":  "MiB",
			},
			"os_type": "image-deleted",
		},
		{
			"id":                "7cd0f695-75b5-4514-82a2-953237e4c7d6",
			"name":              "nsxt-e2e-test-vm-1",
			"status":            "ACTIVE",
			"metadata":          assert.JSONObject{},
			"tags":              []string{},
			"availability_zone": "az-one",
			"hypervisor":        "vmware",
			"flavor":            "g_c8_m16",
			"vcpu":              8,
			"ram": assert.JSONObject{
				"value": 16368,
				"unit":  "MiB",
			},
			"disk": assert.JSONObject{
				"value": 64,
				"unit":  "GiB",
			},
			"video_ram": assert.JSONObject{
				"value": 16,
				"unit":  "MiB",
			},
			"os_type": "image-deleted",
		},
	}

	testSubresourceTranslation(t, "nova-flavors", subresourcesInLiquidFormat, subresourcesInLegacyFormat)
}

func testSubresourceTranslation(t *testing.T, ruleID string, subresourcesInLiquidFormat, subresourcesInLegacyFormat []assert.JSONObject, opts ...test.SetupOption) {
	localOpts := []test.SetupOption{
		test.WithDBFixtureFile("fixtures/start-data-small.sql"),
		test.WithConfig(testSmallConfigYAML),
		test.WithAPIHandler(api.NewV1API),
		test.WithMockLiquidClient("first", test.DefaultLiquidServiceInfo()),
	}
	opts = append(localOpts, opts...)
	s := test.NewSetup(t,
		opts...,
	)
	s.Cluster.Config.ResourceBehaviors = []core.ResourceBehavior{{
		FullResourceNameRx:     "first/capacity",
		TranslationRuleInV1API: must.Return(core.NewTranslationRule(ruleID)),
	}}

	_, err := s.DB.Exec(`UPDATE project_az_resources SET subresources = $1 WHERE az_resource_id = (SELECT id FROM az_resources WHERE path = $2)`,
		string(must.Return(json.Marshal(subresourcesInLiquidFormat))),
		"first/capacity/az-one",
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
