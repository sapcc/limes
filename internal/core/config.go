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
	"os"

	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/errext"
	"github.com/sapcc/go-bits/regexpext"
	yaml "gopkg.in/yaml.v2"

	"github.com/sapcc/limes/internal/util"
)

// ClusterConfiguration contains all the configuration data for a single
// cluster. It is instantiated from YAML and then transformed into type
// Cluster during the startup phase.
type ClusterConfiguration struct {
	CatalogURL string                   `yaml:"catalog_url"`
	Discovery  DiscoveryConfiguration   `yaml:"discovery"`
	Services   []ServiceConfiguration   `yaml:"services"`
	Capacitors []CapacitorConfiguration `yaml:"capacitors"`
	//^ Sorry for the stupid pun. Not.
	Subresources             map[string][]string               `yaml:"subresources"`
	Subcapacities            map[string][]string               `yaml:"subcapacities"`
	LowPrivilegeRaise        LowPrivilegeRaiseConfiguration    `yaml:"lowpriv_raise"`
	ResourceBehaviors        []ResourceBehavior                `yaml:"resource_behavior"`
	Bursting                 BurstingConfiguration             `yaml:"bursting"`
	QuotaDistributionConfigs []*QuotaDistributionConfiguration `yaml:"quota_distribution_configs"`
}

// GetServiceConfigurationForType returns the ServiceConfiguration or an error.
func (cluster *ClusterConfiguration) GetServiceConfigurationForType(serviceType string) (ServiceConfiguration, error) {
	for _, svc := range cluster.Services {
		if serviceType == svc.ServiceType {
			return svc, nil
		}
	}
	return ServiceConfiguration{}, fmt.Errorf("no configuration found for service %s", serviceType)
}

// DiscoveryConfiguration describes the method of discovering Keystone domains
// and projects.
type DiscoveryConfiguration struct {
	Method          string                `yaml:"method"`
	ExcludeDomainRx regexpext.PlainRegexp `yaml:"except_domains"`
	IncludeDomainRx regexpext.PlainRegexp `yaml:"only_domains"`
	Parameters      util.YamlRawMessage   `yaml:"params"`
}

// ServiceConfiguration describes a service that is enabled for a certain cluster.
type ServiceConfiguration struct {
	ServiceType string `yaml:"service_type"`
	PluginType  string `yaml:"type"`
	// RateLimits describes the global rate limits (all requests for to a backend) and default project level rate limits.
	RateLimits ServiceRateLimitConfiguration `yaml:"rate_limits"`
	Parameters util.YamlRawMessage           `yaml:"params"`
}

// ServiceRateLimitConfiguration describes the global and project-level default rate limit configurations for a service.
type ServiceRateLimitConfiguration struct {
	Global         []RateLimitConfiguration `yaml:"global"`
	ProjectDefault []RateLimitConfiguration `yaml:"project_default"`
}

// GetProjectDefaultRateLimit returns the default project-level rate limit for a given target type URI and action or an error if not found.
func (svcRlConfig *ServiceRateLimitConfiguration) GetProjectDefaultRateLimit(name string) (RateLimitConfiguration, bool) {
	for _, rateCfg := range svcRlConfig.ProjectDefault {
		if rateCfg.Name == name {
			return rateCfg, true
		}
	}
	return RateLimitConfiguration{}, false
}

// RateLimitConfiguration describes a rate limit configuration.
type RateLimitConfiguration struct {
	Name   string            `yaml:"name"`
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

// BurstingConfiguration contains the configuration options for quota bursting.
type BurstingConfiguration struct {
	//If MaxMultiplier is zero, bursting is disabled.
	MaxMultiplier limesresources.BurstingMultiplier `yaml:"max_multiplier"`
}

// QuotaDistributionConfiguration contains configuration options for specifying
// the QuotaDistributionModel of specific resources.
type QuotaDistributionConfiguration struct {
	FullResourceNameRx     regexpext.BoundedRegexp               `yaml:"resource"`
	Model                  limesresources.QuotaDistributionModel `yaml:"model"`
	DefaultProjectQuota    uint64                                `yaml:"default_project_quota"` //required for CentralizedQuotaDistribution
	StrictDomainQuotaLimit bool                                  `yaml:"strict_domain_quota_limit"`
}

// InitialProjectQuota returns the quota value that will be assigned to new
// project resources governed by this QuotaDistributionConfiguration.
func (c QuotaDistributionConfiguration) InitialProjectQuota() uint64 {
	switch c.Model {
	case limesresources.HierarchicalQuotaDistribution:
		return 0
	case limesresources.CentralizedQuotaDistribution:
		return c.DefaultProjectQuota
	default:
		panic(fmt.Sprintf("invalid quota distribution model: %q", c.Model))
	}
}

// NewConfiguration reads and validates the given configuration file.
// Errors are logged and will result in
// program termination, causing the function to not return.
func NewConfiguration(path string) (cluster *Cluster, errs errext.ErrorSet) {
	//read config file
	configBytes, err := os.ReadFile(path)
	if err != nil {
		errs.Addf("read configuration file: %s", err.Error())
		return nil, errs
	}
	var config ClusterConfiguration
	err = yaml.UnmarshalStrict(configBytes, &config)
	if err != nil {
		errs.Addf("parse configuration: %s", err.Error())
		return nil, errs
	}

	//cannot proceed if the config is not valid
	errs.Append(config.validateConfig())
	if !errs.IsEmpty() {
		return nil, errs
	}

	//inflate the ClusterConfiguration instances into Cluster, thereby validating
	//the existence of the requested quota and capacity plugins and initializing
	//some handy lookup tables
	if config.Discovery.Method == "" {
		//choose default discovery method
		config.Discovery.Method = "list"
	}
	return NewCluster(config)
}

func (cluster ClusterConfiguration) validateConfig() (errs errext.ErrorSet) {
	missing := func(key string) {
		errs.Addf("missing configuration value: %s", key)
	}

	if len(cluster.Services) == 0 {
		missing("services[]")
	}
	//NOTE: cluster.Capacitors is optional

	for idx, srv := range cluster.Services {
		if srv.ServiceType == "" {
			missing(fmt.Sprintf("services[%d].id", idx))
		}
		if srv.PluginType == "" {
			missing(fmt.Sprintf("services[%d].type", idx))
		}
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

	for idx, qdCfg := range cluster.QuotaDistributionConfigs {
		if qdCfg.FullResourceNameRx == "" {
			missing(fmt.Sprintf(`distribution_model_configs[%d].resource`, idx))
		}

		switch qdCfg.Model {
		case limesresources.HierarchicalQuotaDistribution:
			if qdCfg.DefaultProjectQuota != 0 {
				errs.Addf("distribution_model_configs[%d].default_project_quota is invalid: not allowed for hierarchical distribution", idx)
			}
		case limesresources.CentralizedQuotaDistribution:
			if qdCfg.DefaultProjectQuota == 0 {
				missing(fmt.Sprintf(`distribution_model_configs[%d].default_project_quota`, idx))
			}
			if qdCfg.StrictDomainQuotaLimit {
				errs.Addf("invalid value for distribution_model_configs[%d].strict_domain_quota_limit: not allowed for centralized distribution", idx)
			}
		default:
			errs.Addf("invalid value for distribution_model_configs[%d].model: %q", idx, qdCfg.Model)
		}
	}

	if cluster.Bursting.MaxMultiplier < 0 {
		errs.Addf("bursting.max_multiplier may not be negative")
	}

	return errs
}
