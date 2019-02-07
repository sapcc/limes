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
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math"
	"os"
	"regexp"
	"strings"

	policy "github.com/databus23/goslo.policy"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/limes"
	"github.com/sapcc/limes/pkg/db"

	yaml "gopkg.in/yaml.v2"
)

//Configuration contains all the data from the configuration file.
type Configuration struct {
	Database  db.Configuration       `yaml:"database"`
	Clusters  map[string]*Cluster    `yaml:"-"`
	API       APIConfiguration       `yaml:"api"`
	Collector CollectorConfiguration `yaml:"collector"`
}

type configurationInFile struct {
	Database  db.Configuration                 `yaml:"database"`
	Clusters  map[string]*ClusterConfiguration `yaml:"clusters"`
	API       APIConfiguration                 `yaml:"api"`
	Collector CollectorConfiguration           `yaml:"collector"`
}

//ClusterConfiguration contains all the configuration data for a single cluster.
//It is passed around in a lot of Limes code, mostly for the cluster ID and the
//list of enabled services.
type ClusterConfiguration struct {
	Auth       *AuthParameters          `yaml:"auth"`
	CatalogURL string                   `yaml:"catalog_url"`
	Discovery  DiscoveryConfiguration   `yaml:"discovery"`
	Services   []ServiceConfiguration   `yaml:"services"`
	Capacitors []CapacitorConfiguration `yaml:"capacitors"`
	//^ Sorry for the stupid pun. Not.
	Subresources          map[string][]string              `yaml:"subresources"`
	Subcapacities         map[string][]string              `yaml:"subcapacities"`
	Authoritative         bool                             `yaml:"authoritative"`
	ConstraintConfigPaths []string                         `yaml:"constraints"`
	CADF                  CADFConfiguration                `yaml:"cadf"`
	LowPrivilegeRaise     LowPrivilegeRaiseConfiguration   `yaml:"lowpriv_raise"`
	ResourceBehaviors     []*ResourceBehaviorConfiguration `yaml:"resource_behavior"`
	Bursting              BurstingConfiguration            `yaml:"bursting"`
	//The following is only read to warn that users need to upgrade from seeds to constraints.
	OldSeedConfigPath string `yaml:"seeds"`
}

//DiscoveryConfiguration describes the method of discovering Keystone domains
//and projects.
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
}

//ServiceConfiguration describes a service that is enabled for a certain cluster.
type ServiceConfiguration struct {
	Type   string          `yaml:"type"`
	Shared bool            `yaml:"shared"`
	Auth   *AuthParameters `yaml:"auth"`
	//for quota plugins that need configuration, add a field with the service type as
	//name and put the config data in there (use a struct to be able to give
	//config options meaningful names)
	Compute struct {
		HypervisorTypeRules []struct {
			Key     string `yaml:"match"`
			Pattern string `yaml:"pattern"`
			Type    string `yaml:"type"`
		} `yaml:"hypervisor_type_rules"`
	} `yaml:"compute"`
	CFM struct {
		Authoritative bool `yaml:"authoritative"`
	} `yaml:"database"`
}

//CapacitorConfiguration describes a capacity plugin that is enabled for a
//certain cluster.
type CapacitorConfiguration struct {
	ID string `yaml:"id"`
	//for capacitors that need configuration, add a field with the plugin's ID as
	//name and put the config data in there (use a struct to be able to give
	//config options meaningful names)
	Nova struct {
		ExtraSpecs            map[string]string `yaml:"extra_specs"`
		HypervisorTypePattern string            `yaml:"hypervisor_type_pattern"`
	} `yaml:"nova"`
	Prometheus struct {
		APIURL  string                       `yaml:"api_url"`
		Queries map[string]map[string]string `yaml:"queries"`
	} `yaml:"prometheus"`
	Cinder struct {
		VolumeBackendName string `yaml:"volume_backend_name"`
	} `yaml:"cinder"`
	Manila struct {
		ShareNetworks     uint64  `yaml:"share_networks"`
		SharesPerPool     uint64  `yaml:"shares_per_pool"`
		SnapshotsPerShare uint64  `yaml:"snapshots_per_share"`
		CapacityBalance   float64 `yaml:"capacity_balance"`
	} `yaml:"manila"`
	Manual map[string]map[string]uint64 `yaml:"manual"`
}

//LowPrivilegeRaiseConfiguration contains the configuration options for
//low-privilege quota raising in a certain cluster.
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

//IsAllowedForProjectsIn checks if low-privilege quota raising is enabled by this config
//for the domain with the given name.
func (l LowPrivilegeRaiseConfiguration) IsAllowedForProjectsIn(domainName string) bool {
	if l.ExcludeProjectDomainRx != nil && l.ExcludeProjectDomainRx.MatchString(domainName) {
		return false
	}
	if l.IncludeProjectDomainRx == nil {
		return true
	}
	return l.IncludeProjectDomainRx.MatchString(domainName)
}

//ResourceBehaviorConfiguration contains the configuration options for
//specialized behaviors of a single resource (or a set of resources) in a
//certain cluster.
type ResourceBehaviorConfiguration struct {
	FullResourceName   string                    `yaml:"resource"`
	MaxBurstMultiplier *limes.BurstingMultiplier `yaml:"max_burst_multiplier"`
	OvercommitFactor   float64                   `yaml:"overcommit_factor"`
	ScalesWith         string                    `yaml:"scales_with"`
	ScalingFactor      float64                   `yaml:"scaling_factor"`
	Compiled           ResourceBehavior          `yaml:"-"`
}

//ResourceBehavior is the compiled version of ResourceBehaviorConfiguration.
type ResourceBehavior struct {
	FullResourceName       *regexp.Regexp
	MaxBurstMultiplier     limes.BurstingMultiplier
	OvercommitFactor       float64
	ScalesWithResourceName string
	ScalesWithServiceType  string
	ScalingFactor          float64
}

//ToScalingBehavior returns the limes.ScalingBehavior for this resource, or nil
//if no scaling has been configured.
func (b ResourceBehavior) ToScalingBehavior() *limes.ScalingBehavior {
	if b.ScalesWithResourceName == "" {
		return nil
	}
	return &limes.ScalingBehavior{
		ScalesWithServiceType:  b.ScalesWithServiceType,
		ScalesWithResourceName: b.ScalesWithResourceName,
		ScalingFactor:          b.ScalingFactor,
	}
}

//BurstingConfiguration contains the configuration options for quota bursting.
type BurstingConfiguration struct {
	//If MaxMultiplier is zero, bursting is disabled.
	MaxMultiplier limes.BurstingMultiplier `yaml:"max_multiplier"`
}

//CADFConfiguration contains configuration parameters for audit trail.
type CADFConfiguration struct {
	Enabled  bool `yaml:"enabled"`
	RabbitMQ struct {
		URL       string `yaml:"url"`
		QueueName string `yaml:"queue_name"`
	} `yaml:"rabbitmq"`
}

//APIConfiguration contains configuration parameters for limes-serve.
type APIConfiguration struct {
	ListenAddress  string                `yaml:"listen"`
	PolicyFilePath string                `yaml:"policy"`
	PolicyEnforcer gopherpolicy.Enforcer `yaml:"-"`
	RequestLog     struct {
		ExceptStatusCodes []int `yaml:"except_status_codes"`
	} `yaml:"request_log"`
	CORS struct {
		AllowedOrigins []string `yaml:"allowed_origins"`
	} `yaml:"cors"`
}

//CollectorConfiguration contains configuration parameters for limes-collect.
type CollectorConfiguration struct {
	MetricsListenAddress   string `yaml:"metrics"`
	ExposeDataMetrics      bool   `yaml:"data_metrics"`
	SkipZeroForDataMetrics bool   `yaml:"data_metrics_skip_zero"`
}

//NewConfiguration reads and validates the given configuration file.
//Errors are logged and will result in
//program termination, causing the function to not return.
func NewConfiguration(path string) (cfg Configuration) {
	//read config file
	configBytes, err := ioutil.ReadFile(path)
	if err != nil {
		logg.Fatal("read configuration file: %s", err.Error())
	}
	var cfgFile configurationInFile
	err = yaml.Unmarshal(configBytes, &cfgFile)
	if err != nil {
		logg.Fatal("parse configuration: %s", err.Error())
	}
	if !cfgFile.validate() {
		os.Exit(1)
	}

	//inflate the ClusterConfiguration instances into Cluster, thereby validating
	//the existence of the requested quota and capacity plugins and initializing
	//some handy lookup tables
	cfg = Configuration{
		Database:  cfgFile.Database,
		Clusters:  make(map[string]*Cluster),
		API:       cfgFile.API,
		Collector: cfgFile.Collector,
	}
	for clusterID, config := range cfgFile.Clusters {
		if config.Discovery.Method == "" {
			//choose default discovery method
			config.Discovery.Method = "list"
		}
		cfg.Clusters[clusterID] = NewCluster(clusterID, config)
	}

	//load the policy file
	cfg.API.PolicyEnforcer, err = loadPolicyFile(cfg.API.PolicyFilePath)
	if err != nil {
		logg.Fatal(err.Error())
	}

	return
}

func (cfg configurationInFile) validate() (success bool) {
	//do not fail on first error; keep going and report all errors at once
	success = true //until proven otherwise

	missing := func(key string) {
		logg.Error("missing %s configuration value", key)
		success = false
	}
	if cfg.Database.Location == "" {
		missing("database.location")
	}
	if len(cfg.Clusters) == 0 {
		missing("clusters[]")
	}

	for clusterID, cluster := range cfg.Clusters {
		switch clusterID {
		case "current":
			logg.Error("\"current\" is not an acceptable cluster ID (it would make the URL /v1/clusters/current ambiguous)")
			success = false
		case "shared":
			logg.Error("\"shared\" is not an acceptable cluster ID (it is used for internal accounting)")
			success = false
		}

		missing := func(key string) {
			logg.Error("missing clusters[%s].%s configuration value", clusterID, key)
			success = false
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

		if cluster.Auth == nil {
			//Avoid nil pointer access if section cluster.auth not provided but still alert on the missing values
			cluster.Auth = new(AuthParameters)
		}
		//gophercloud is very strict about requiring a trailing slash here
		if cluster.Auth.AuthURL != "" && !strings.HasSuffix(cluster.Auth.AuthURL, "/") {
			cluster.Auth.AuthURL += "/"
		}

		switch {
		case cluster.Auth.AuthURL == "":
			missing("auth.auth_url")
		case !strings.HasPrefix(cluster.Auth.AuthURL, "http://") && !strings.HasPrefix(cluster.Auth.AuthURL, "https://"):
			logg.Error("clusters[%s].auth.auth_url does not look like a HTTP URL", clusterID)
			success = false
		case !strings.HasSuffix(cluster.Auth.AuthURL, "/v3/"):
			logg.Error("clusters[%s].auth.auth_url does not end with \"/v3/\"", clusterID)
			success = false
		}

		if cluster.Auth.UserName == "" {
			missing("auth.user_name")
		}
		if cluster.Auth.UserDomainName == "" {
			missing("auth.user_domain_name")
		}
		if cluster.Auth.ProjectName == "" {
			missing("auth.project_name")
		}
		if cluster.Auth.ProjectDomainName == "" {
			missing("auth.project_domain_name")
		}
		if cluster.Auth.Password == "" {
			missing("auth.password")
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
		}
		for idx, capa := range cluster.Capacitors {
			if capa.ID == "" {
				missing(fmt.Sprintf("capacitors[%d].id", idx))
			}
		}

		cluster.Discovery.IncludeDomainRx = compileOptionalRx(cluster.Discovery.IncludeDomainPattern)
		cluster.Discovery.ExcludeDomainRx = compileOptionalRx(cluster.Discovery.ExcludeDomainPattern)

		cluster.LowPrivilegeRaise.IncludeProjectDomainRx = compileOptionalRx(cluster.LowPrivilegeRaise.IncludeProjectDomainPattern)
		cluster.LowPrivilegeRaise.ExcludeProjectDomainRx = compileOptionalRx(cluster.LowPrivilegeRaise.ExcludeProjectDomainPattern)

		for idx, behavior := range cluster.ResourceBehaviors {
			behavior.Compiled = ResourceBehavior{
				OvercommitFactor: behavior.OvercommitFactor,
			}

			if behavior.FullResourceName == "" {
				missing(fmt.Sprintf(`resource_behavior[%d].resource`, idx))
			} else {
				pattern := `^` + behavior.FullResourceName + `$`
				behavior.Compiled.FullResourceName = compileOptionalRx(pattern)
			}

			if behavior.MaxBurstMultiplier != nil {
				behavior.Compiled.MaxBurstMultiplier = *behavior.MaxBurstMultiplier
				if *behavior.MaxBurstMultiplier < 0 {
					logg.Error(`clusters[%s].resource_behavior[%d].max_burst_multiplier may not be negative`, clusterID, idx)
					success = false
				}
			} else {
				behavior.Compiled.MaxBurstMultiplier = limes.BurstingMultiplier(math.Inf(+1))
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
						logg.Error(`clusters[%s].resource_behavior[%d].scales_with must have the format "service_type/resource_name"`, clusterID, idx)
						success = false
					}
				}
			}
		}

		if cluster.Bursting.MaxMultiplier < 0 {
			logg.Error("clusters[%s].bursting.max_multiplier may not be negative")
			success = false
		}

		//warn about removed configuration options
		if cluster.OldSeedConfigPath != "" {
			logg.Error("quota seeds have been replaced by quota constraints: rename clusters[%s].seeds config key to clusters[%s].constraints and convert seed file into constraint file; documentation at https://github.com/sapcc/limes/blob/master/docs/operators/constraints.md", clusterID, clusterID)
			success = false
		}
	}

	if cfg.API.ListenAddress == "" {
		missing("api.listen")
	}
	if cfg.API.PolicyFilePath == "" {
		missing("api.policy")
	}

	if cfg.Collector.MetricsListenAddress == "" {
		missing("collector.metrics")
	}

	return
}

func loadPolicyFile(path string) (gopherpolicy.Enforcer, error) {
	bytes, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rules map[string]string
	err = json.Unmarshal(bytes, &rules)
	if err != nil {
		return nil, err
	}
	return policy.NewEnforcer(rules)
}
