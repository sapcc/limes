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
	"github.com/gophercloud/gophercloud"
)

//KeystoneDomain describes the basic attributes of a Keystone domain.
type KeystoneDomain struct {
	UUID string `json:"id"`
	Name string `json:"name"`
}

//KeystoneProject describes the basic attributes of a Keystone project.
type KeystoneProject struct {
	UUID       string `json:"id"`
	Name       string `json:"name"`
	ParentUUID string `json:"parent_id"`
}

//DiscoveryPlugin is the interface that the collector uses to discover Keystone
//projects and domains in a cluster.
type DiscoveryPlugin interface {
	//Method returns a unique identifier for this DiscoveryPlugin which is used to
	//identify it in the configuration.
	Method() string
	//ListDomains returns all Keystone domains in the cluster.
	ListDomains(client *gophercloud.ProviderClient) ([]KeystoneDomain, error)
	//ListProjects returns all Keystone projects in the given domain.
	ListProjects(client *gophercloud.ProviderClient, domainUUID string) ([]KeystoneProject, error)
}

//ResourceData contains quota and usage data for a single resource.
//
//The Subresources field may optionally be populated with subresources, if the
//quota plugin providing this ResourceData instance has been instructed to (and
//is able to) scrape subresources for this resource.
type ResourceData struct {
	Quota        int64 //negative values indicate infinite quota
	Usage        uint64
	Subresources []interface{}
}

//QuotaPlugin is the interface that the quota/usage collector plugins for all
//backend services must implement. There can only be one QuotaPlugin for each
//backend service.
type QuotaPlugin interface {
	//Init is guaranteed to be called before all other methods exposed by the
	//interface. Implementations can use it f.i. to discover the available
	//Resources().
	Init(client *gophercloud.ProviderClient) error
	//ServiceInfo returns metadata for this service.
	ServiceInfo() ServiceInfo
	//Resources returns metadata for all the resources that this plugin scrapes
	//from the backend service.
	Resources() []ResourceInfo
	//Scrape queries the backend service for the quota and usage data of all
	//known resources for the given project in the given domain. The string keys
	//in the result map must be identical to the resource names
	//from Resources().
	//
	//The clusterID is usually not needed, but should be given as a label to
	//Prometheus metrics emitted by the plugin (if the plugin does that sort of
	//thing).
	Scrape(client *gophercloud.ProviderClient, clusterID, domainUUID, projectUUID string) (map[string]ResourceData, error)
	//SetQuota updates the backend service's quotas for the given project in the
	//given domain to the values specified here. The map is guaranteed to contain
	//values for all resources defined by Resources().
	//
	//The clusterID is usually not needed, but should be given as a label to
	//Prometheus metrics emitted by the plugin (if the plugin does that sort of
	//thing).
	SetQuota(client *gophercloud.ProviderClient, clusterID, domainUUID, projectUUID string, quotas map[string]uint64) error
}

//CapacityData contains capacity data for a single resource.
//
//The Subcapacities field may optionally be populated with subcapacities, if the
//capacity plugin providing this CapacityData instance has been instructed to (and
//is able to) scrape subcapacities for this resource.
type CapacityData struct {
	Capacity      uint64
	Subcapacities []interface{}
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
	//
	//The clusterID is usually not needed, but should be given as a label to
	//Prometheus metrics emitted by the plugin (if the plugin does that sort of
	//thing).
	Scrape(client *gophercloud.ProviderClient, clusterID string) (map[string]map[string]CapacityData, error)
}

//ResourceInfo contains the metadata for a resource (i.e. some thing for which
//quota and usage values can be retrieved from a backend service).
type ResourceInfo struct {
	Name string `json:"name"`
	Unit Unit   `json:"unit,omitempty"`
	//Category is an optional hint that UIs can use to group resources of one
	//service into subgroups. If it is used, it should be set on all
	//ResourceInfos reported by the same QuotaPlugin.
	Category string `json:"category,omitempty"`
	//If AutoApproveInitialQuota is non-zero, when a new project is scraped for
	//the first time, a backend quota equal to this value will be approved
	//automatically (i.e. Quota will be set equal to BackendQuota).
	AutoApproveInitialQuota uint64 `json:"-"`
}

//ServiceInfo contains the metadata for a backend service.
type ServiceInfo struct {
	//Type returns the service type that the backend service for this
	//plugin implements. This string must be identical to the type string from
	//the Keystone service catalog.
	Type string `json:"type"`
	//ProductName returns the name of the product that is the reference
	//implementation for this service. For example, ProductName = "nova" for
	//Type = "compute".
	ProductName string `json:"-"`
	//Area is a hint that UIs can use to group similar services.
	Area string `json:"area"`
}

//DiscoveryPluginFactory is a function that produces discovery plugins with a
//certain ID. The discovery plugin instance will use the discovery configuration
//given to it if it wants to.
type DiscoveryPluginFactory func(DiscoveryConfiguration) DiscoveryPlugin

//QuotaPluginFactory is a function that produces quota plugins for a certain
//ServiceInfo.Type. The quota plugin instance will use the service
//configuration given to it if it wants to. For plugins that support
//subresource scraping, the second argument indicates which resources to scrape
//(the keys are resource names).
type QuotaPluginFactory func(cfg ServiceConfiguration, scrapeSubresources map[string]bool) QuotaPlugin

//CapacityPluginFactory is a function that produces capacity plugins with a
//certain ID. The capacity plugin instance will use the capacitor configuration
//given to it if it wants to. For plugins that support subcapacity scraping,
//the second argument indicates which resources to scrape (the first key is the
//service type, the second key is the resource name).
type CapacityPluginFactory func(cfg CapacitorConfiguration, scrapeSubcapacities map[string]map[string]bool) CapacityPlugin

var discoveryPluginFactories = map[string]DiscoveryPluginFactory{}
var quotaPluginFactories = map[string]QuotaPluginFactory{}
var capacityPluginFactories = map[string]CapacityPluginFactory{}
var serviceTypesByArea = map[string][]string{}

//RegisterDiscoveryPlugin registers a DiscoveryPlugin with this package. It may
//only be called once, typically in a func init() for the package that offers
//the DiscoveryPlugin.
//
//When called, this function will use the factory with a zero
//ServiceConfiguration to determine the ServiceType of the quota plugin.
func RegisterDiscoveryPlugin(factory DiscoveryPluginFactory) {
	if factory == nil {
		panic("collector.RegisterDiscoveryPlugin() called with nil DiscoveryPluginFactory instance")
	}
	method := factory(DiscoveryConfiguration{}).Method()
	if method == "" {
		panic("DiscoveryPlugin instance with empty Method!")
	}
	if discoveryPluginFactories[method] != nil {
		panic("collector.RegisterDiscoveryPlugin() called multiple times for method: " + method)
	}
	discoveryPluginFactories[method] = factory
}

//RegisterQuotaPlugin registers a QuotaPlugin with this package. It may only be
//called once, typically in a func init() for the package that offers the
//QuotaPlugin.
//
//When called, this function will use the factory with a zero
//ServiceConfiguration to determine the service type of the quota plugin.
func RegisterQuotaPlugin(factory QuotaPluginFactory) {
	if factory == nil {
		panic("collector.RegisterQuotaPlugin() called with nil QuotaPluginFactory instance")
	}
	info := factory(ServiceConfiguration{}, map[string]bool{}).ServiceInfo()
	if info.Type == "" {
		panic("QuotaPlugin instance with empty service type!")
	}
	if info.Area == "" {
		panic("QuotaPlugin instance with empty area!")
	}
	if quotaPluginFactories[info.Type] != nil {
		panic("collector.RegisterQuotaPlugin() called multiple times for service type: " + info.Type)
	}
	quotaPluginFactories[info.Type] = factory
	serviceTypesByArea[info.Area] = append(serviceTypesByArea[info.Area], info.Type)
}

//GetServiceTypesForArea returns a list of all service types whose QuotaPlugins
//report the given area.
func GetServiceTypesForArea(area string) []string {
	return serviceTypesByArea[area]
}

//RegisterCapacityPlugin registers a CapacityPlugin with this package. It may
//only be called once, typically in a func init() for the package that offers
//the CapacityPlugin.
//
//When called, this function will use the factory with a zero
//CapacitorConfiguration to determine the ID of the capacity plugin.
func RegisterCapacityPlugin(factory CapacityPluginFactory) {
	if factory == nil {
		panic("collector.RegisterCapacityPlugin() called with nil CapacityPluginFactory instance")
	}
	id := factory(CapacitorConfiguration{}, nil).ID()
	if id == "" {
		panic("CapacityPlugin instance with empty ID!")
	}
	if capacityPluginFactories[id] != nil {
		panic("collector.RegisterCapacityPlugin() called multiple times for ID: " + id)
	}
	capacityPluginFactories[id] = factory
}
