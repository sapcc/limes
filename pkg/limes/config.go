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

package limes

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"strings"

	policy "github.com/databus23/goslo.policy"
	"github.com/gophercloud/gophercloud"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/util"

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
	AuthURL           string                   `yaml:"auth_url"`
	UserName          string                   `yaml:"user_name"`
	UserDomainName    string                   `yaml:"user_domain_name"`
	ProjectName       string                   `yaml:"project_name"`
	ProjectDomainName string                   `yaml:"project_domain_name"`
	Password          string                   `yaml:"password"`
	RegionName        string                   `yaml:"region_name"`
	CatalogURL        string                   `yaml:"catalog_url"`
	Services          []ServiceConfiguration   `yaml:"services"`
	Capacitors        []CapacitorConfiguration `yaml:"capacitors"`
	//Sorry for the stupid pun. Not.
}

//ServiceConfiguration describes a service that is enabled for a certain cluster.
type ServiceConfiguration struct {
	Type   string `yaml:"type"`
	Shared bool   `yaml:"shared"`
}

//CapacitorConfiguration describes a capacity plugin that is enabled for a
//certain cluster.
type CapacitorConfiguration struct {
	ID   string `yaml:"id"`
	Nova struct {
		VCPUOvercommitFactor *uint64 `yaml:"vcpu_overcommit"`
	} `yaml:"nova"`
}

//APIConfiguration contains configuration parameters for limes-serve.
type APIConfiguration struct {
	ListenAddress  string           `yaml:"listen"`
	PolicyFilePath string           `yaml:"policy"`
	PolicyEnforcer *policy.Enforcer `yaml:"-"`
}

//CollectorConfiguration contains configuration parameters for limes-collect.
type CollectorConfiguration struct {
	MetricsListenAddress string `yaml:"metrics"`
	ExposeDataMetrics    bool   `yaml:"data_metrics"`
}

//NewConfiguration reads and validates the given configuration file.
//Errors are logged and will result in
//program termination, causing the function to not return.
func NewConfiguration(path string) (cfg Configuration) {
	//read config file
	configBytes, err := ioutil.ReadFile(path)
	if err != nil {
		util.LogFatal("read configuration file: %s", err.Error())
	}
	var cfgFile configurationInFile
	err = yaml.Unmarshal(configBytes, &cfgFile)
	if err != nil {
		util.LogFatal("parse configuration: %s", err.Error())
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
		cfg.Clusters[clusterID] = NewCluster(clusterID, config)
	}

	//load the policy file
	cfg.API.PolicyEnforcer, err = loadPolicyFile(cfg.API.PolicyFilePath)
	if err != nil {
		util.LogFatal(err.Error())
	}

	return
}

func (cfg configurationInFile) validate() (success bool) {
	//do not fail on first error; keep going and report all errors at once
	success = true //until proven otherwise

	missing := func(key string) {
		util.LogError("missing %s configuration value", key)
		success = false
	}
	if cfg.Database.Location == "" {
		missing("database.location")
	}
	if cfg.Database.MigrationsPath == "" {
		missing("database.migrations")
	}
	if len(cfg.Clusters) == 0 {
		missing("clusters[]")
	}

	for clusterID, cluster := range cfg.Clusters {
		switch clusterID {
		case "current":
			util.LogError("\"current\" is not an acceptable cluster ID (it would make the URL /v1/clusters/current ambiguous)")
			success = false
		case "shared":
			util.LogError("\"shared\" is not an acceptable cluster ID (it is used for internal accounting)")
			success = false
		}

		missing := func(key string) {
			util.LogError("missing clusters[%s].%s configuration value", clusterID, key)
			success = false
		}

		//gophercloud is very strict about requiring a trailing slash here
		if cluster.AuthURL != "" && !strings.HasSuffix(cluster.AuthURL, "/") {
			cluster.AuthURL += "/"
		}

		switch {
		case cluster.AuthURL == "":
			missing("auth_url")
		case !strings.HasPrefix(cluster.AuthURL, "http://") && !strings.HasPrefix(cluster.AuthURL, "https://"):
			util.LogError("clusters[%s].auth_url does not look like a HTTP URL", clusterID)
			success = false
		case !strings.HasSuffix(cluster.AuthURL, "/v3/"):
			util.LogError("clusters[%s].auth_url does not end with \"/v3/\"", clusterID)
			success = false
		}

		if cluster.UserName == "" {
			missing("user_name")
		}
		if cluster.UserDomainName == "" {
			missing("user_domain_name")
		}
		if cluster.ProjectName == "" {
			missing("project_name")
		}
		if cluster.ProjectDomainName == "" {
			missing("project_domain_name")
		}
		if cluster.Password == "" {
			missing("password")
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

func loadPolicyFile(path string) (*policy.Enforcer, error) {
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

//CanReauth implements the
//gophercloud/openstack/identity/v3/tokens.AuthOptionsBuilder interface.
func (cfg *ClusterConfiguration) CanReauth() bool {
	return true
}

//ToTokenV3CreateMap implements the
//gophercloud/openstack/identity/v3/tokens.AuthOptionsBuilder interface.
func (cfg *ClusterConfiguration) ToTokenV3CreateMap(scope map[string]interface{}) (map[string]interface{}, error) {
	gophercloudAuthOpts := gophercloud.AuthOptions{
		Username:    cfg.UserName,
		Password:    cfg.Password,
		DomainName:  cfg.UserDomainName,
		AllowReauth: true,
	}
	return gophercloudAuthOpts.ToTokenV3CreateMap(scope)
}

//ToTokenV3ScopeMap implements the
//gophercloud/openstack/identity/v3/tokens.AuthOptionsBuilder interface.
func (cfg *ClusterConfiguration) ToTokenV3ScopeMap() (map[string]interface{}, error) {
	return map[string]interface{}{
		"project": map[string]interface{}{
			"name":   cfg.ProjectName,
			"domain": map[string]interface{}{"name": cfg.ProjectDomainName},
		},
	}, nil
}
