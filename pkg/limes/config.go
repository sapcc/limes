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
	"io/ioutil"
	"os"
	"strings"

	"github.com/gophercloud/gophercloud"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/util"

	yaml "gopkg.in/yaml.v2"
)

//Configuration contains all the data from the configuration file.
type Configuration struct {
	Database db.Configuration                 `yaml:"database"`
	Clusters map[string]*ClusterConfiguration `yaml:"clusters"`
}

//ClusterConfiguration contains all the configuration data for a single cluster.
//It is passed around in a lot of Limes code, mostly for the cluster ID and the
//list of enabled services.
type ClusterConfiguration struct {
	ID                string `yaml:"-"`
	AuthURL           string `yaml:"auth_url"`
	UserName          string `yaml:"user_name"`
	UserDomainName    string `yaml:"user_domain_name"`
	ProjectName       string `yaml:"project_name"`
	ProjectDomainName string `yaml:"project_domain_name"`
	Password          string `yaml:"password"`
	RegionName        string `yaml:"region_name"`
	Services          []ServiceConfiguration
}

//ServiceConfiguration describes a service that is enabled for a certain cluster.
type ServiceConfiguration struct {
	Type   string `yaml:"type"`
	Shared bool   `yaml:"shared"`
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
	err = yaml.Unmarshal(configBytes, &cfg)
	if err != nil {
		util.LogFatal("parse configuration: %s", err.Error())
	}
	if !cfg.validate() {
		os.Exit(1)
	}

	for clusterID, cluster := range cfg.Clusters {
		//pull the cluster IDs into the ClusterConfiguration objects
		//so that we can then pass around the ClusterConfiguration objects
		//instead of having to juggle both the ID and the config object
		cluster.ID = clusterID
	}
	return
}

func (cfg Configuration) validate() (success bool) {
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
	}

	return
}

////////////////////////////////////////////////////////////////////////////////
// The stuff in here actually belongs into pkg/drivers, but we can only
// implement methods on *ClusterConfiguration here.

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
