// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package core_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/go-bits/errext"

	"github.com/sapcc/limes/internal/core"
)

func TestFilterDomains(t *testing.T) {
	cfg := core.DiscoveryConfiguration{
		IncludeDomainRx: "foo",
		ExcludeDomainRx: "2$",
	}

	input := []core.KeystoneDomain{
		{Name: "bar1"},
		{Name: "bar2"},
		{Name: "foo1"},
		{Name: "foo2"},
	}
	expected := []core.KeystoneDomain{
		{Name: "foo1"},
	}
	assert.DeepEqual(t, "filtered domains", cfg.FilterDomains(input), expected)
}

func TestConfigValidation(t *testing.T) {
	var errs errext.ErrorSet

	// invalid YAML
	_, errs = core.NewClusterFromYAML([]byte(":invalid"), time.Now, nil, true)
	assert.DeepEqual(t, "errs", errs.Join(","), "parse configuration: yaml: unmarshal errors:\n  line 1: cannot unmarshal !!str `:invalid` into core.ClusterConfiguration")

	// empty config
	_, errs = core.NewClusterFromYAML([]byte(""), time.Now, nil, true)
	assert.DeepEqual(t, "errs", errs.Join(","), "missing configuration value: availability_zones[],missing configuration value: liquids[]")

	// invalid AZs
	_, errs = core.NewClusterFromYAML([]byte(strings.ReplaceAll(`
		availability_zones: [ '', 'any' ]
		liquids:
			shared:
				area: testing
	`, "\t", "  ")), time.Now, nil, true)
	assert.DeepEqual(t, "errs", errs.Join(","), "invalid value for availability_zones[0]: \"\" is not an acceptable name for a real AZ,invalid value for availability_zones[1]: \"any\" is not an acceptable name for a real AZ")

	// missing area attribute in liquid config
	_, errs = core.NewClusterFromYAML([]byte(strings.ReplaceAll(`
		availability_zones: [ foo ]
		liquids:
			shared:
	`, "\t", "  ")), time.Now, nil, true)
	assert.DeepEqual(t, "errs", errs.Join(","), "missing configuration value: liquids.shared.area")

	// empty resource/rate behaviors
	_, errs = core.NewClusterFromYAML([]byte(strings.ReplaceAll(`
		availability_zones: [ foo ]
		liquids:
			shared:
				area: testing
		resource_behavior:
			- resource:
		rate_behavior:
			- rate:
	`, "\t", "  ")), time.Now, nil, true)
	assert.DeepEqual(t, "errs", errs.Join(","), "missing configuration value: resource_behavior[0].resource,missing configuration value: rate_behavior[0].rate")

	// quota distribution config: empty resource name and invalid model
	_, errs = core.NewClusterFromYAML([]byte(strings.ReplaceAll(`
		availability_zones: [ foo ]
		liquids:
			shared:
				area: testing
		quota_distribution_configs:
			- { resource: '', model: invalid, autogrow: { growth_multiplier: 1.0, usage_data_retention_period: 48h } }
	`, "\t", "  ")), time.Now, nil, true)
	assert.DeepEqual(t, "errs", errs.Join(","), "missing configuration value: distribution_model_configs[0].resource,invalid value for distribution_model_configs[0].model: \"invalid\",invalid value for distribution_model_configs[0].autogrow: cannot be set for model \"invalid\"")

	// quota distribution config: missing autogrow configuration
	_, errs = core.NewClusterFromYAML([]byte(strings.ReplaceAll(`
		availability_zones: [ foo ]
		liquids:
			shared:
				area: testing
		quota_distribution_configs:
			- { resource: unittest/capacity, model: autogrow }
	`, "\t", "  ")), time.Now, nil, true)
	assert.DeepEqual(t, "errs", errs.Join(","), "missing configuration value: distribution_model_configs[0].autogrow")

	// quota distribution config: invalid growth multiplier and invalid retention period
	_, errs = core.NewClusterFromYAML([]byte(strings.ReplaceAll(`
		availability_zones: [ foo ]
		liquids:
			shared:
				area: testing
		quota_distribution_configs:
			- { resource: shared/capacity, model: autogrow, autogrow: { growth_multiplier: -5.0, usage_data_retention_period: 0h } }
	`, "\t", "  ")), time.Now, nil, true)
	assert.DeepEqual(t, "errs", errs.Join(","), "invalid value for distribution_model_configs[0].growth_multiplier: -5 (must be >= 0),invalid value for distribution_model_configs[0].usage_data_retention_period: must not be 0")

	// commitment conversion: overlapping flavors
	_, errs = core.NewClusterFromYAML([]byte(strings.ReplaceAll(`
		availability_zones: [ az-one, az-two ]
		liquids:
			first:
				area: first
				commitment_behavior_per_resource:
					- key: capacity_c32
						value:
							durations_per_domain: &durations [{ key: '.*', value: ["1 hour", "2 hours"] }]
							conversion_rule: { identifier: flavor1, weight: 32 }
					- key: capacity2_c144
						value:
							durations_per_domain: *durations
							conversion_rule: { identifier: flavor2, weight: 144 }
			second:
				area: second
				commitment_behavior_per_resource:
					- key: capacity_a
						value:
							durations_per_domain: *durations
							conversion_rule: { identifier: flavor2, weight: 48 }
	`, "\t", "  ")), time.Now, nil, true)
	assert.DeepEqual(t, "errs", errs.Join(","), `invalid value: liquids.second.commitment_behavior_per_resource[0].conversion_rule.identifier values must be restricted to a single serviceType, but "flavor2" is already used by another serviceType`)

	// Valid config. Empty discovery method should be allowed
	_, errs = core.NewClusterFromYAML([]byte(strings.ReplaceAll(`
		availability_zones: [ foo ]
		discovery:
			method: ""
		liquids:
			shared:
				area: testing
			unshared:
				area: testing
		resource_behavior:
			- { resource: shared/capacity, overcommit_factor: 10.0 }
			- resource: unshared/capacity
				identity_in_v1_api: service/resource
		quota_distribution_configs:
			- { resource: '.*/capacity', model: autogrow, autogrow: { growth_multiplier: 1.0, project_base_quota: 10, usage_data_retention_period: 1m } }
	`, "\t", "  ")), time.Now, nil, true)
	if !errs.IsEmpty() {
		t.Errorf("expected no errors for a valid configuration but got: %s", errs.Join(","))
	}
}
