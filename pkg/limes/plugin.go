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

import "strconv"

//QuotaPlugin is the interface that the quota/usage collector plugins for all
//backend services must implement. There can only be one QuotaPlugin for each
//backend service.
type QuotaPlugin interface {
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
	//SetQuota updates the backend service's quotas for the given project in the
	//given domain to the values specified here. The map is guaranteed to contain
	//values for all resources defined by Resources().
	SetQuota(driver Driver, domainUUID, projectUUID string, quotas map[string]uint64) error
}

//CapacityPlugin is the interface that all capacity collector plugins must
//implement.
//
//While there can only be one QuotaPlugin for each backend service, there may
//be different CapacityPlugin instances for each backend service, and a single
//CapacityPlugin can even report capacities for multiple service types. The
//reason is that quotas are handled in the concrete backend service, thus their
//handling is independent from the underlying infrastructure. Capacity
//calculations, however, may be highly dependent on the infrastructure. For
//example, for the Compute service, there could be different capacity plugins
//for each type of hypervisor (KVM, VMware, etc.) which use the concrete APIs
//of these hypervisors instead of the OpenStack Compute API.
type CapacityPlugin interface {
	//ID returns a unique identifier for this CapacityPlugin which is used to
	//identify it in the configuration.
	ID() string
	//Scrape queries the backend service(s) for the capacities of the resources
	//that this plugin is concerned with. The result is a two-dimensional map,
	//with the first key being the service type, and the second key being the
	//resource name. The capacity collector will ignore service types for which
	//there is no QuotaPlugin, and resources which are not advertised by that
	//QuotaPlugin.
	Scrape(driver Driver) (map[string]map[string]uint64, error)
}

//ResourceInfo contains the metadata for a resource (i.e. some thing for which
//quota and usage values can be retrieved from a backend service).
type ResourceInfo struct {
	Name string
	Unit Unit
	//If AutoApproveInitialQuota is non-zero, when a new project is scraped for
	//the first time, a backend quota equal to this value will be approved
	//automatically (i.e. Quota will be set equal to BackendQuota).
	AutoApproveInitialQuota uint64
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

//Format appends the unit (if any) to the given value. This should only be used
//for error messages; actual UIs should be more clever about formatting values
//(e.g. UnitMebibytes.Format(1048576) returns "1048576 MiB" where "1 TiB"
//would be more appropriate).
func (u Unit) Format(value uint64) string {
	str := strconv.FormatUint(value, 10)
	if u == UnitNone {
		return str
	}
	return str + " " + string(u)
}

//UnitFor finds the plugin for the given serviceType and finds within that
//plugin the ResourceInfo for the given resourceName, and returns its unit. If
//the service or resource does not exist, UnitNone is returned.
func UnitFor(serviceType, resourceName string) Unit {
	plugin := GetQuotaPlugin(serviceType)
	if plugin == nil {
		return UnitNone
	}
	for _, res := range plugin.Resources() {
		if res.Name == resourceName {
			return res.Unit
		}
	}
	return UnitNone
}

var quotaPlugins = map[string]QuotaPlugin{}
var capacityPlugins = map[string]CapacityPlugin{}

//RegisterQuotaPlugin registers a QuotaPlugin with this package. It may only be
//called once, typically in a func init() for the package that offers the
//QuotaPlugin.
func RegisterQuotaPlugin(plugin QuotaPlugin) {
	if plugin == nil {
		panic("collector.RegisterQuotaPlugin() called with nil Plugin instance")
	}
	serviceType := plugin.ServiceType()
	if quotaPlugins[serviceType] != nil {
		panic("collector.RegisterQuotaPlugin() called multiple times for service type: " + serviceType)
	}
	quotaPlugins[serviceType] = plugin
}

//GetQuotaPlugin returns the QuotaPlugin that handles the given service type,
//or nil if no such plugin exists.
func GetQuotaPlugin(serviceType string) QuotaPlugin {
	return quotaPlugins[serviceType]
}

//RegisterCapacityPlugin registers a CapacityPlugin with this package. It may
//only be called once, typically in a func init() for the package that offers
//the CapacityPlugin.
func RegisterCapacityPlugin(plugin CapacityPlugin) {
	if plugin == nil {
		panic("collector.RegisterCapacityPlugin() called with nil Plugin instance")
	}
	id := plugin.ID()
	if capacityPlugins[id] != nil {
		panic("collector.RegisterCapacityPlugin() called multiple times for ID: " + id)
	}
	capacityPlugins[id] = plugin
}

//GetCapacityPlugin returns the CapacityPlugin with the given ID, or nil if no
//such plugin exists.
func GetCapacityPlugin(serviceType string) CapacityPlugin {
	return capacityPlugins[serviceType]
}
