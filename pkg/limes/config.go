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

	yaml "gopkg.in/yaml.v2"
)

//Configuration contains all the data from the configuration file.
type Configuration struct {
	Database ConfigurationEntryDatabase           `yaml:"database"`
	Clusters map[string]ConfigurationEntryCluster `yaml:"clusters"`
}

//ConfigurationEntryDatabase is used inside type Configuration, and only has an
//exported name to produce more readable error messages for malformed YAMLs.
type ConfigurationEntryDatabase struct {
	Location       string `yaml:"location"`
	MigrationsPath string `yaml:"migrations"`
}

//ConfigurationEntryCluster is used inside type Configuration, and only has an
//exported name to produce more readable error messages for malformed YAMLs.
type ConfigurationEntryCluster struct {
	AuthURL           string `yaml:"auth_url"`
	UserName          string `yaml:"user_name"`
	UserDomainName    string `yaml:"user_domain_name"`
	ProjectName       string `yaml:"project_name"`
	ProjectDomainName string `yaml:"project_domain_name"`
	Password          string `yaml:"password"`
	RegionName        string `yaml:"region_name"`
	Services          []ConfigurationEntryService
}

//ConfigurationEntryService is used inside type Configuration, and only has an
//exported name to produce more readable error messages for malformed YAMLs.
type ConfigurationEntryService struct {
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
		Log(LogFatal, "read configuration file: %s", err.Error())
	}
	err = yaml.Unmarshal(configBytes, &cfg)
	if err != nil {
		Log(LogFatal, "parse configuration: %s", err.Error())
	}
	if !cfg.validate() {
		os.Exit(1)
	}
	return
}

func (cfg Configuration) validate() (success bool) {
	//do not fail on first error; keep going and report all errors at once
	success = true //until proven otherwise

	missing := func(key string) {
		Log(LogError, "missing %s configuration value", key)
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
			Log(LogError, "missing clusters[%s].%s configuration value", clusterID, key)
			success = false
		}

		switch {
		case cluster.AuthURL == "":
			missing("auth_url")
		case !strings.HasPrefix(cluster.AuthURL, "http://") && !strings.HasPrefix(cluster.AuthURL, "https://"):
			Log(LogError, "clusters[%s].auth_url does not look like a HTTP URL", clusterID)
			success = false
		case !strings.HasSuffix(strings.TrimSuffix(cluster.AuthURL, "/"), "/v3"):
			Log(LogError, "clusters[%s].auth_url does not end with \"/v3\"", clusterID)
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
