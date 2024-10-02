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

package core

import (
	"fmt"
	"time"

	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/errext"
	"github.com/sapcc/go-bits/regexpext"
	yaml "gopkg.in/yaml.v2"

	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"
)

// ClusterConfiguration contains all the configuration data for a single
// cluster. It is instantiated from YAML and then transformed into type
// Cluster during the startup phase.
type ClusterConfiguration struct {
	AvailabilityZones []limes.AvailabilityZone `yaml:"availability_zones"`
	CatalogURL        string                   `yaml:"catalog_url"`
	Discovery         DiscoveryConfiguration   `yaml:"discovery"`
	Services          []ServiceConfiguration   `yaml:"services"`
	Capacitors        []CapacitorConfiguration `yaml:"capacitors"`
	// ^ Sorry for the stupid pun. Not.
	ResourceBehaviors []ResourceBehavior `yaml:"resource_behavior"`
	RateBehaviors     []RateBehavior     `yaml:"rate_behavior"`
}

// GetServiceConfigurationForType returns the ServiceConfiguration or false.
func (cluster *ClusterConfiguration) GetServiceConfigurationForType(serviceType db.ServiceType) (ServiceConfiguration, bool) {
	for _, svc := range cluster.Services {
		if serviceType == svc.ServiceType {
			return svc, true
		}
	}
	return ServiceConfiguration{}, false
}

// DiscoveryConfiguration describes the method of discovering Keystone domains
// and projects.
type DiscoveryConfiguration struct {
	Method          string                `yaml:"method"`
	ExcludeDomainRx regexpext.PlainRegexp `yaml:"except_domains"`
	IncludeDomainRx regexpext.PlainRegexp `yaml:"only_domains"`
	Parameters      util.YamlRawMessage   `yaml:"params"`
}

// FilterDomains applies the configured ExcludeDomainRx and IncludeDomainRx to
// the given list of domains.
func (c DiscoveryConfiguration) FilterDomains(domains []KeystoneDomain) []KeystoneDomain {
	result := make([]KeystoneDomain, 0, len(domains))
	for _, domain := range domains {
		if c.ExcludeDomainRx != "" {
			if c.ExcludeDomainRx.MatchString(domain.Name) {
				continue
			}
		}
		if c.IncludeDomainRx != "" {
			if !c.IncludeDomainRx.MatchString(domain.Name) {
				continue
			}
		}
		result = append(result, domain)
	}
	return result
}

// ServiceConfiguration describes a service that is enabled for a certain cluster.
type ServiceConfiguration struct {
	ServiceType db.ServiceType      `yaml:"service_type"`
	PluginType  string              `yaml:"type"`
	Parameters  util.YamlRawMessage `yaml:"params"`

	// Resource-specific configuration.
	QuotaDistributionConfigs  regexpext.ConfigSet[liquid.ResourceName, QuotaDistributionConfiguration] `yaml:"quota_distribution_config_for_resource"`
	CommitmentCreationRules   regexpext.ConfigSet[liquid.ResourceName, CommitmentCreationRule]         `yaml:"commitment_creation_rule_for_resource"`
	CommitmentConversionRules regexpext.ConfigSet[liquid.ResourceName, CommitmentConversionRule]       `yaml:"commitment_conversion_rule_for_resource"`
	TranslationRulesInV1API   regexpext.ConfigSet[liquid.ResourceName, TranslationRule]                `yaml:"translation_rule_in_v1_api_for_resource"`

	// RateLimits describes the global rate limits (all requests for to a backend) and default project level rate limits.
	RateLimits ServiceRateLimitConfiguration `yaml:"rate_limits"`
}

// ServiceRateLimitConfiguration describes the global and project-level default rate limit configurations for a service.
type ServiceRateLimitConfiguration struct {
	Global         []RateLimitConfiguration `yaml:"global"`
	ProjectDefault []RateLimitConfiguration `yaml:"project_default"`
}

// GetProjectDefaultRateLimit returns the default project-level rate limit for a given target type URI and action or an error if not found.
func (svcRlConfig *ServiceRateLimitConfiguration) GetProjectDefaultRateLimit(name liquid.RateName) (RateLimitConfiguration, bool) {
	for _, rateCfg := range svcRlConfig.ProjectDefault {
		if rateCfg.Name == name {
			return rateCfg, true
		}
	}
	return RateLimitConfiguration{}, false
}

// RateLimitConfiguration describes a rate limit configuration.
type RateLimitConfiguration struct {
	Name   liquid.RateName   `yaml:"name"`
	Unit   limes.Unit        `yaml:"unit"`
	Limit  uint64            `yaml:"limit"`
	Window limesrates.Window `yaml:"window"`
}

// CapacitorConfiguration describes a capacity plugin that is enabled for a
// certain cluster.
type CapacitorConfiguration struct {
	ID         string              `yaml:"id"`
	PluginType string              `yaml:"type"`
	Parameters util.YamlRawMessage `yaml:"params"`
}

// QuotaDistributionConfiguration contains configuration options for specifying
// the QuotaDistributionModel of specific resources.
type QuotaDistributionConfiguration struct {
	Model limesresources.QuotaDistributionModel `yaml:"model"`
	// options for AutogrowQuotaDistribution
	Autogrow *AutogrowQuotaDistributionConfiguration `yaml:"autogrow"`
}

// CommitmentCreationRule contains configuration options for
// the creation of commitments on specific resources.
type CommitmentCreationRule struct {
	Durations      []limesresources.CommitmentDuration `yaml:"durations"`
	IsAZAware      bool                                `yaml:"is_az_aware"`
	MinConfirmDate *time.Time                          `yaml:"min_confirm_date"`
	UntilPercent   *float64                            `yaml:"until_percent"`
}

// ToAPI returns the API representation for this commitment configuration,
// or nil if commitments are not allowed on the respective resource.
func (ccr CommitmentCreationRule) ToAPI(now time.Time) *limesresources.CommitmentConfiguration {
	if len(ccr.Durations) == 0 {
		return nil
	}
	result := limesresources.CommitmentConfiguration{
		Durations: ccr.Durations,
	}
	if ccr.MinConfirmDate != nil && ccr.MinConfirmDate.After(now) {
		result.MinConfirmBy = &limes.UnixEncodedTime{Time: *ccr.MinConfirmDate}
	}
	return &result
}

// CommitmentConversionRule contains configuration options for
// the conversion of commitments on specific resources.
type CommitmentConversionRule struct {
	Identifier string `yaml:"identifier"`
	Weight     uint64 `yaml:"weight"`
}

// AutogrowQuotaDistributionConfiguration appears in type QuotaDistributionConfiguration.
type AutogrowQuotaDistributionConfiguration struct {
	AllowQuotaOvercommitUntilAllocatedPercent float64                      `yaml:"allow_quota_overcommit_until_allocated_percent"`
	ProjectBaseQuota                          uint64                       `yaml:"project_base_quota"`
	GrowthMultiplier                          float64                      `yaml:"growth_multiplier"`
	GrowthMinimum                             uint64                       `yaml:"growth_minimum"`
	UsageDataRetentionPeriod                  util.MarshalableTimeDuration `yaml:"usage_data_retention_period"`
}

// NewClusterFromYAML reads and validates the configuration in the given YAML document.
// Errors are logged and will result in program termination, causing the function to not return.
func NewClusterFromYAML(configBytes []byte) (cluster *Cluster, errs errext.ErrorSet) {
	var config ClusterConfiguration
	err := yaml.UnmarshalStrict(configBytes, &config)
	if err != nil {
		errs.Addf("parse configuration: %w", err)
		return nil, errs
	}

	// cannot proceed if the config is not valid
	errs.Append(config.validateConfig())
	if !errs.IsEmpty() {
		return nil, errs
	}

	// inflate the ClusterConfiguration instances into Cluster, thereby validating
	// the existence of the requested quota and capacity plugins and initializing
	// some handy lookup tables
	if config.Discovery.Method == "" {
		// choose default discovery method
		config.Discovery.Method = "list"
	}
	return NewCluster(config)
}

func (cluster ClusterConfiguration) validateConfig() (errs errext.ErrorSet) {
	missing := func(key string) {
		errs.Addf("missing configuration value: %s", key)
	}

	if len(cluster.AvailabilityZones) == 0 {
		missing("availability_zones[]")
	}
	if len(cluster.Services) == 0 {
		missing("services[]")
	}
	//NOTE: cluster.Capacitors is optional

	for _, srv := range cluster.Services {
		errs.Append(srv.validateConfig())
	}

	for idx, capa := range cluster.Capacitors {
		if capa.ID == "" {
			missing(fmt.Sprintf("capacitors[%d].id", idx))
		}
		if capa.PluginType == "" {
			missing(fmt.Sprintf("capacitors[%d].type", idx))
		}
	}

	for idx, behavior := range cluster.ResourceBehaviors {
		errs.Append(behavior.Validate(fmt.Sprintf("resource_behavior[%d]", idx)))
	}
	for idx, behavior := range cluster.RateBehaviors {
		errs.Append(behavior.Validate(fmt.Sprintf("rate_behavior[%d]", idx)))
	}

	return errs
}

func (srv ServiceConfiguration) validateConfig() (errs errext.ErrorSet) {
	// for use in error messages below
	srvPath := fmt.Sprintf(`services[service_type=%q]`, srv.ServiceType)
	missing := func(key string) {
		errs.Addf("missing configuration value: %s.%s", srvPath, key)
	}

	if srv.ServiceType == "" {
		missing("service_type")
	}
	if srv.PluginType == "" {
		missing("type")
	}

	for idx, qdCfg := range srv.QuotaDistributionConfigs {
		if qdCfg.Key == "" {
			missing(fmt.Sprintf(`quota_distribution_config_for_resource[%d].key`, idx))
		}
		qdPath := fmt.Sprintf(`%s.quota_distribution_config_for_resource[key=%q].value`, srvPath, qdCfg.Key)

		switch qdCfg.Value.Model {
		case limesresources.AutogrowQuotaDistribution:
			if qdCfg.Value.Autogrow == nil {
				missing(qdPath + ".autogrow")
			}
			autogrow := *qdCfg.Value.Autogrow
			if autogrow.GrowthMultiplier < 0 {
				errs.Addf("invalid value for %s.growth_multiplier: %g (must be >= 0)", qdPath, autogrow.GrowthMultiplier)
			}
			if autogrow.UsageDataRetentionPeriod.Into() == 0 {
				errs.Addf("invalid value for %s.usage_data_retention_period: must not be 0", qdPath)
			}
		default:
			errs.Addf("invalid value for %s.model: %q", qdPath, qdCfg.Value.Model)
		}

		if qdCfg.Value.Model != limesresources.AutogrowQuotaDistribution && qdCfg.Value.Autogrow != nil {
			errs.Addf("invalid value for %s.autogrow: cannot be set for model %q", qdPath, qdCfg.Value.Model)
		}
	}

	for idx, ccr := range srv.CommitmentCreationRules {
		if ccr.Key == "" {
			missing(fmt.Sprintf(`commitment_creation_rule_for_resource[%d].key`, idx))
		}
		ccrPath := fmt.Sprintf(`%s.commitment_creation_rule_for_resource[key=%q].value`, srvPath, ccr.Key)

		if ccr.Value.UntilPercent != nil && *ccr.Value.UntilPercent > 100 {
			errs.Addf("invalid value: %s.commitment_until_percent may not be bigger than 100", ccrPath)
		}
	}

	for idx, ccr := range srv.CommitmentConversionRules {
		if ccr.Key == "" {
			missing(fmt.Sprintf(`commitment_conversion_rule_for_resource[%d].key`, idx))
		}
		ccrPath := fmt.Sprintf(`%s.commitment_conversion_rule_for_resource[key=%q].value`, srvPath, ccr.Key)

		if ccr.Value.Identifier != "" && ccr.Value.Weight == 0 {
			errs.Addf("invalid value: %s.weight must not be 0", ccrPath)
		}
	}

	for idx, tr := range srv.TranslationRulesInV1API {
		if tr.Key == "" {
			missing(fmt.Sprintf(`translation_rule_in_v1_api_for_resource[%d].key`, idx))
		}
	}

	return errs
}
