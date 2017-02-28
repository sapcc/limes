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

	yaml "gopkg.in/yaml.v2"
)

//Configuration contains all the data from the configuration file.
type Configuration struct {
	DatabaseConnString string `yaml:"database"`
	MigrationDirPath   string `yaml:"migrations"`
}

//Config contains the global configuration for this process. It must be
//initialized with a call to InitConfiguration().
var Config Configuration

//InitConfiguration reads the given configuration file and populates the
//`Config` variable in this package.
func InitConfiguration(path string) {
	//read config file
	configBytes, err := ioutil.ReadFile(path)
	if err != nil {
		Log(LogFatal, "read configuration file: %s", err.Error())
	}
	err = yaml.Unmarshal(configBytes, &Config)
	if err != nil {
		Log(LogFatal, "parse configuration: %s", err.Error())
	}

	if Config.DatabaseConnString == "" {
		Log(LogFatal, "missing limes.database configuration value")
	}
	if Config.MigrationDirPath == "" {
		Log(LogFatal, "missing limes.migrations configuration value")
	}
}
