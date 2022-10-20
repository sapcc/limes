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
	"math"
	"os"
	"regexp"
	"strings"

	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/logg"
	yaml "gopkg.in/yaml.v2"
)

// ClusterConfiguration contains all the configuration data for a single cluster.
// It is passed around in a lot of Limes code, mostly for the cluster ID and the
// list of enabled services.
type ClusterConfiguration struct {
	ClusterID  string                   `yaml:"cluster_id"`
	CatalogURL string                   `yaml:"catalog_url"`
	Discovery  DiscoveryConfiguration   `yaml:"discovery"`
	Services   []ServiceConfiguration   `yaml:"services"`
	Capacitors []CapacitorConfiguration `yaml:"capacitors"`
	//^ Sorry for the stupid pun. Not.
	Subresources             map[string][]string               `yaml:"subresources"`
	Subcapacities            map[string][]string               `yaml:"subcapacities"`
	LowPrivilegeRaise        LowPrivilegeRaiseConfiguration    `yaml:"lowpriv_raise"`
	ResourceBehaviors        []*ResourceBehaviorConfiguration  `yaml:"resource_behavior"`
	Bursting                 BurstingConfiguration             `yaml:"bursting"`
	QuotaDistributionConfigs []*QuotaDistributionConfiguration `yaml:"quota_distribution_configs"`
}

// GetServiceConfigurationForType returns the ServiceConfiguration or an error.
func (cluster *ClusterConfiguration) GetServiceConfigurationForType(serviceType string) (ServiceConfiguration, error) {
	for _, svc := range cluster.Services {
		if serviceType == svc.Type {
			return svc, nil
		}
	}
	return ServiceConfiguration{}, fmt.Errorf("no configuration found for service %s", serviceType)
}

// DiscoveryConfiguration describes the method of discovering Keystone domains
// and projects.
type DiscoveryConfiguration struct {
	Method               string         `yaml:"method"`
	ExcludeDomainPattern string         `yaml:"except_domains"`
	IncludeDomainPattern string         `yaml:"only_domains"`
	ExcludeDomainRx      *regexp.Regexp `yaml:"-"`
	IncludeDomainRx      *regexp.Regexp `yaml:"-"`
	//for discovery methods that need configuration, add a field with the method
	//as name and put the config data in there (use a struct to be able to give
	//config options meaningful names)
	RoleAssignment struct {
		RoleName string `yaml:"role"`
	} `yaml:"role-assignment"`
	Static struct {
		Domains []struct {
			UUID     string `yaml:"id"`
			Name     string `yaml:"name"`
			Projects []struct {
				UUID       string `yaml:"id"`
				Name       string `yaml:"name"`
				ParentUUID string `yaml:"parent_id"`
			} `yaml:"projects"`
		} `yaml:"domains"`
	} `yaml:"static"`
}

// ServiceConfiguration describes a service that is enabled for a certain cluster.
type ServiceConfiguration struct {
	Type   string                 `yaml:"type"`
	Shared bool                   `yaml:"shared"`
	Auth   map[string]interface{} `yaml:"auth"`
	// RateLimits describes the global rate limits (all requests for to a backend) and default project level rate limits.
	RateLimits ServiceRateLimitConfiguration `yaml:"rate_limits"`
	//for quota plugins that need configuration, add a field with the service type as
	//name and put the config data in there (use a struct to be able to give
	//config options meaningful names)
	Compute struct {
		BigVMMinMemoryMiB   uint64 `yaml:"bigvm_min_memory"`
		HypervisorTypeRules []struct {
			Key     string `yaml:"match"`
			Pattern string `yaml:"pattern"`
			Type    string `yaml:"type"`
		} `yaml:"hypervisor_type_rules"`
		SeparateInstanceQuotas struct {
			FlavorNamePattern string              `yaml:"flavor_name_pattern"`
			FlavorAliases     map[string][]string `yaml:"flavor_aliases"`
		} `yaml:"separate_instance_quotas"`
	} `yaml:"compute"`
	CFM struct {
		Authoritative bool `yaml:"authoritative"`
		//TODO: remove this hidden feature flag when we have migrated to the new reporting style everywhere
		ReportPhysicalUsage bool `yaml:"report_physical_usage"`
	} `yaml:"database"`
	ShareV2 struct {
		ShareTypes          []ManilaShareTypeSpec       `yaml:"share_types"`
		PrometheusAPIConfig *PrometheusAPIConfiguration `yaml:"prometheus_api"`
	} `yaml:"sharev2"`
	VolumeV2 struct {
		VolumeTypes []string `yaml:"volume_types"`
	} `yaml:"volumev2"`
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
	ID   string                 `yaml:"id"`
	Type string                 `yaml:"type"`
	Auth map[string]interface{} `yaml:"auth"`
	//for capacitors that need configuration, add a field with the plugin's ID as
	//name and put the config data in there (use a struct to be able to give
	//config options meaningful names)
	Nova struct {
		ExtraSpecs            map[string]string `yaml:"extra_specs"`
		HypervisorTypePattern string            `yaml:"hypervisor_type_pattern"`
		UsePlacementAPI       bool              `yaml:"use_placement_api"`
	} `yaml:"nova"`
	Prometheus struct {
		APIConfig PrometheusAPIConfiguration   `yaml:"api"`
		Queries   map[string]map[string]string `yaml:"queries"`
	} `yaml:"prometheus"`
	Cinder struct {
		VolumeTypes map[string]struct {
			VolumeBackendName string `yaml:"volume_backend_name"`
			IsDefault         bool   `yaml:"default"`
		} `yaml:"volume_types"`
	} `yaml:"cinder"`
	Manila struct {
		ShareTypes        []ManilaShareTypeSpec `yaml:"share_types"`
		ShareNetworks     uint64                `yaml:"share_networks"`
		SharesPerPool     uint64                `yaml:"shares_per_pool"`
		SnapshotsPerShare uint64                `yaml:"snapshots_per_share"`
		CapacityBalance   float64               `yaml:"capacity_balance"`
	} `yaml:"manila"`
	Manual      map[string]map[string]uint64 `yaml:"manual"`
	SAPCCIronic struct {
		FlavorAliases map[string][]string `yaml:"flavor_aliases"`
	} `yaml:"sapcc_ironic"`
}

// ManilaMappingRule appears in both ServiceConfiguration and CapacitorConfiguration.
type ManilaShareTypeSpec struct {
	Name               string `yaml:"name"`
	ReplicationEnabled bool   `yaml:"replication_enabled"` //only used by QuotaPlugin
	MappingRules       []*struct {
		NamePattern string         `yaml:"name_pattern"`
		NameRx      *regexp.Regexp `yaml:"-"`
		ShareType   string         `yaml:"share_type"`
	} `yaml:"mapping_rules"`
}

// LowPrivilegeRaiseConfiguration contains the configuration options for
// low-privilege quota raising in a certain cluster.
type LowPrivilegeRaiseConfiguration struct {
	Limits struct {
		ForDomains  map[string]map[string]string `yaml:"domains"`
		ForProjects map[string]map[string]string `yaml:"projects"`
	} `yaml:"limits"`
	ExcludeProjectDomainPattern string         `yaml:"except_projects_in_domains"`
	IncludeProjectDomainPattern string         `yaml:"only_projects_in_domains"`
	IncludeProjectDomainRx      *regexp.Regexp `yaml:"-"`
	ExcludeProjectDomainRx      *regexp.Regexp `yaml:"-"`
}

// IsAllowedForProjectsIn checks if low-privilege quota raising is enabled by this config
// for the domain with the given name.
func (l LowPrivilegeRaiseConfiguration) IsAllowedForProjectsIn(domainName string) bool {
	if l.ExcludeProjectDomainRx != nil && l.ExcludeProjectDomainRx.MatchString(domainName) {
		return false
	}
	if l.IncludeProjectDomainRx == nil {
		return true
	}
	return l.IncludeProjectDomainRx.MatchString(domainName)
}

// ResourceBehaviorConfiguration contains the configuration options for
// specialized behaviors of a single resource (or a set of resources) in a
// certain cluster.
type ResourceBehaviorConfiguration struct {
	FullResourceName       string                             `yaml:"resource"`
	Scope                  string                             `yaml:"scope"`
	MaxBurstMultiplier     *limesresources.BurstingMultiplier `yaml:"max_burst_multiplier"`
	OvercommitFactor       float64                            `yaml:"overcommit_factor"`
	ScalesWith             string                             `yaml:"scales_with"`
	ScalingFactor          float64                            `yaml:"scaling_factor"`
	MinNonZeroProjectQuota uint64                             `yaml:"min_nonzero_project_quota"`
	Annotations            map[string]interface{}             `yaml:"annotations"`
	Compiled               ResourceBehavior                   `yaml:"-"`
}

// ResourceBehavior is the compiled version of ResourceBehaviorConfiguration.
type ResourceBehavior struct {
	FullResourceNameRx     *regexp.Regexp
	ScopeRx                *regexp.Regexp
	MaxBurstMultiplier     limesresources.BurstingMultiplier
	OvercommitFactor       float64
	ScalesWithResourceName string
	ScalesWithServiceType  string
	ScalingFactor          float64
	MinNonZeroProjectQuota uint64
	Annotations            map[string]interface{}
}

// ToScalingBehavior returns the limes.ScalingBehavior for this resource, or nil
// if no scaling has been configured.
func (b ResourceBehavior) ToScalingBehavior() *limesresources.ScalingBehavior {
	if b.ScalesWithResourceName == "" {
		return nil
	}
	return &limesresources.ScalingBehavior{
		ScalesWithServiceType:  b.ScalesWithServiceType,
		ScalesWithResourceName: b.ScalesWithResourceName,
		ScalingFactor:          b.ScalingFactor,
	}
}

// BurstingConfiguration contains the configuration options for quota bursting.
type BurstingConfiguration struct {
	//If MaxMultiplier is zero, bursting is disabled.
	MaxMultiplier limesresources.BurstingMultiplier `yaml:"max_multiplier"`
}

// PrometheusAPIConfiguration contains configuration parameters for a Prometheus API.
// Only the URL field is required in the format: "http<s>://localhost<:9090>" (port is optional).
type PrometheusAPIConfiguration struct {
	URL                      string `yaml:"url"`
	ClientCertificatePath    string `yaml:"cert"`
	ClientCertificateKeyPath string `yaml:"key"`
	ServerCACertificatePath  string `yaml:"ca_cert"`
}

// QuotaDistributionConfiguration contains configuration options for specifying
// the QuotaDistributionModel of specific resources.
type QuotaDistributionConfiguration struct {
	FullResourceName    string                                `yaml:"resource"`
	FullResourceNameRx  *regexp.Regexp                        `yaml:"-"`
	Model               limesresources.QuotaDistributionModel `yaml:"model"`
	DefaultProjectQuota uint64                                `yaml:"default_project_quota"` //required for CentralizedQuotaDistribution
}

// NewConfiguration reads and validates the given configuration file.
// Errors are logged and will result in
// program termination, causing the function to not return.
func NewConfiguration(path string) (cluster *Cluster) {
	//read config file
	configBytes, err := os.ReadFile(path)
	if err != nil {
		logg.Fatal("read configuration file: %s", err.Error())
	}
	var config ClusterConfiguration
	err = yaml.Unmarshal(configBytes, &config)
	if err != nil {
		logg.Fatal("parse configuration: %s", err.Error())
	}
	if !config.validateConfig() {
		os.Exit(1)
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

func (cluster ClusterConfiguration) validateConfig() (success bool) {
	//do not fail on first error; keep going and report all errors at once
	success = true //until proven otherwise

	missing := func(key string) {
		logg.Error("missing %s configuration value", key)
		success = false
	}
	if cluster.ClusterID == "" {
		missing("cluster_id")
	}

	compileOptionalRx := func(pattern string) *regexp.Regexp {
		if pattern == "" {
			return nil
		}
		rx, err := regexp.Compile(pattern)
		if err != nil {
			logg.Error("failed to compile regex %#v: %s", pattern, err.Error())
			success = false
		}
		return rx
	}

	//NOTE: cluster.RegionName is optional
	if len(cluster.Services) == 0 {
		missing("services[]")
	}
	//NOTE: cluster.Capacitors is optional

	for idx, srv := range cluster.Services {
		if srv.Type == "" {
			missing(fmt.Sprintf("services[%d].type", idx))
		}
		if srv.Shared {
			//TODO remove this deprecation warning once the change was rolled out everywhere
			logg.Error("services[%d].shared is true, which is not supported anymore", idx)
			success = false
		}
		if len(srv.Auth) > 0 {
			//TODO remove this deprecation warning once the change was rolled out everywhere
			logg.Error("services[%d].auth was provided, but is not supported anymore", idx)
			success = false
		}
	}
	for idx, capa := range cluster.Capacitors {
		if capa.ID == "" {
			missing(fmt.Sprintf("capacitors[%d].id", idx))
		}
		if capa.Type == "" {
			missing(fmt.Sprintf("capacitors[%d].type", idx))
		}
		if len(capa.Auth) > 0 {
			//TODO remove this deprecation warning once the change was rolled out everywhere
			logg.Error("capacitors[%d].auth was provided, but is not supported anymore", idx)
			success = false
		}
	}

	cluster.Discovery.IncludeDomainRx = compileOptionalRx(cluster.Discovery.IncludeDomainPattern)
	cluster.Discovery.ExcludeDomainRx = compileOptionalRx(cluster.Discovery.ExcludeDomainPattern)

	cluster.LowPrivilegeRaise.IncludeProjectDomainRx = compileOptionalRx(cluster.LowPrivilegeRaise.IncludeProjectDomainPattern)
	cluster.LowPrivilegeRaise.ExcludeProjectDomainRx = compileOptionalRx(cluster.LowPrivilegeRaise.ExcludeProjectDomainPattern)

	for idx, behavior := range cluster.ResourceBehaviors {
		behavior.Compiled = ResourceBehavior{
			OvercommitFactor:       behavior.OvercommitFactor,
			MinNonZeroProjectQuota: behavior.MinNonZeroProjectQuota,
			Annotations:            behavior.Annotations,
		}

		if behavior.FullResourceName == "" {
			missing(fmt.Sprintf(`resource_behavior[%d].resource`, idx))
		} else {
			pattern := `^(?:` + behavior.FullResourceName + `)$`
			behavior.Compiled.FullResourceNameRx = compileOptionalRx(pattern)
		}

		if behavior.Scope == "" {
			behavior.Compiled.ScopeRx = nil
		} else {
			pattern := `^(?:` + behavior.Scope + `)$`
			behavior.Compiled.ScopeRx = compileOptionalRx(pattern)
		}

		if behavior.MaxBurstMultiplier != nil {
			behavior.Compiled.MaxBurstMultiplier = *behavior.MaxBurstMultiplier
			if *behavior.MaxBurstMultiplier < 0 {
				logg.Error(`resource_behavior[%d].max_burst_multiplier may not be negative`, idx)
				success = false
			}
		} else {
			behavior.Compiled.MaxBurstMultiplier = limesresources.BurstingMultiplier(math.Inf(+1))
		}

		if behavior.ScalesWith != "" {
			if behavior.ScalingFactor == 0 {
				missing(fmt.Sprintf(
					`resource_behavior[%d].scaling_factor (must be given since "scales_with" is given)`,
					idx,
				))
			} else {
				if strings.Contains(behavior.ScalesWith, "/") {
					fields := strings.SplitN(behavior.ScalesWith, "/", 2)
					behavior.Compiled.ScalesWithServiceType = fields[0]
					behavior.Compiled.ScalesWithResourceName = fields[1]
					behavior.Compiled.ScalingFactor = behavior.ScalingFactor
				} else {
					logg.Error(`resource_behavior[%d].scales_with must have the format "service_type/resource_name"`, idx)
					success = false
				}
			}
		}
	}

	for idx, qdCfg := range cluster.QuotaDistributionConfigs {
		if qdCfg.FullResourceName == "" {
			missing(fmt.Sprintf(`distribution_model_configs[%d].resource`, idx))
		} else {
			pattern := `^(?:` + qdCfg.FullResourceName + `)$`
			qdCfg.FullResourceNameRx = compileOptionalRx(pattern)
		}

		switch qdCfg.Model {
		case limesresources.HierarchicalQuotaDistribution:
			if qdCfg.DefaultProjectQuota != 0 {
				logg.Error("distribution_model_configs[%d].default_project_quota is invalid: not allowed for hierarchical distribution", idx)
			}
		case limesresources.CentralizedQuotaDistribution:
			if qdCfg.DefaultProjectQuota == 0 {
				missing(fmt.Sprintf(`distribution_model_configs[%d].default_project_quota`, idx))
			}
		default:
			logg.Error("invalid value for distribution_model_configs[%d].model: %q", idx, qdCfg.Model)
			success = false
		}
	}

	if cluster.Bursting.MaxMultiplier < 0 {
		logg.Error("bursting.max_multiplier may not be negative")
		success = false
	}

	return
}
