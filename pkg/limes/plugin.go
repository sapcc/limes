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

//Plugin is an interface that the collector plugins for all backend services must
//implement.
type Plugin interface {
	//ServiceType returns the service type that the backend service for this
	//plugin implements. This string must be identical to the type string from
	//the Keystone service catalog.
	ServiceType() string
	//Resources returns metadata for all the resources that this plugin scrapes
	//from the backend service.
	Resources() []ResourceInfo
	//Scrape queries the backend service for the quota and usage data of all
	//known resources for the given project in the given domain. The string keys
	//in the result map must be identical to the resource names
	//from Resources().
	Scrape(driver Driver, domainUUID, projectUUID string) (map[string]ResourceData, error)
	//Capacity queries the backend service for the total capacity of its
	//resources. If, for certain resources, a capacity estimate is not possible,
	//the implementation shall omit these resources from the result. The string
	//keys in the result map must be identical to the resource names from
	//Resources().
	Capacity(driver Driver) (map[string]uint64, error)
}

//ResourceInfo contains the metadata for a resource (i.e. some thing for which
//quota and usage values can be retrieved from a backend service).
type ResourceInfo struct {
	Name string
	Unit Unit
}

//Unit enumerates allowed values for the unit a resource's quota/usage is
//measured in.
type Unit string

const (
	//UnitNone is used for countable (rather than measurable) resources.
	UnitNone Unit = ""
	//UnitBytes is exactly that.
	UnitBytes Unit = "B"
	//UnitKibibytes is exactly that.
	UnitKibibytes Unit = "KiB"
	//UnitMebibytes is exactly that.
	UnitMebibytes Unit = "MiB"
	//UnitGibibytes is exactly that.
	UnitGibibytes Unit = "GiB"
	//UnitTebibytes is exactly that.
	UnitTebibytes Unit = "TiB"
	//UnitPebibytes is exactly that.
	UnitPebibytes Unit = "PiB"
	//UnitExbibytes is exactly that.
	UnitExbibytes Unit = "EiB"
)

var plugins = map[string]Plugin{}

//RegisterPlugin registers a Plugin with this package. It should only be called
//once, typically in a func init() for the package that offers the Plugin. The
//service type must be identical to the type string used in the Keystone
//service catalog for the backend service that this plugin supports.
func RegisterPlugin(plugin Plugin) {
	serviceType := plugin.ServiceType()
	if plugins[serviceType] != nil {
		panic("collector.RegisterPlugin() called multiple times for service type: " + serviceType)
	}
	if plugin == nil {
		panic("collector.RegisterPlugin() called with nil Plugin instance")
	}
	plugins[serviceType] = plugin
}

//GetPlugin returns the Plugin that handles the given service type, or nil if
//no such plugin exists.
func GetPlugin(serviceType string) Plugin {
	return plugins[serviceType]
}

//AllPlugins lists all service types for which plugins exist.
func AllPlugins() []string {
	result := make([]string, 0, len(plugins))
	for serviceType := range plugins {
		result = append(result, serviceType)
	}
	return result
}
