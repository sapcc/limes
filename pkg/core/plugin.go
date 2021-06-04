/*******************************************************************************
*
* Copyright 2017-2020 SAP SE
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
	"math/big"

	"github.com/gophercloud/gophercloud"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/limes"
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
	ListDomains(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) ([]KeystoneDomain, error)
	//ListProjects returns all Keystone projects in the given domain.
	ListProjects(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, domainUUID string) ([]KeystoneProject, error)
}

//ResourceData contains quota and usage data for a single resource.
//
//The Subresources field may optionally be populated with subresources, if the
//quota plugin providing this ResourceData instance has been instructed to (and
//is able to) scrape subresources for this resource.
type ResourceData struct {
	Quota         int64 //negative values indicate infinite quota
	Usage         uint64
	PhysicalUsage *uint64 //only supported by some plugins
	Subresources  []interface{}
}

//QuotaPlugin is the interface that the quota/usage collector plugins for all
//backend services must implement. There can only be one QuotaPlugin for each
//backend service.
type QuotaPlugin interface {
	//Init is guaranteed to be called before all other methods exposed by the
	//interface. Implementations can use it f.i. to discover the available
	//Resources().
	Init(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error
	//ServiceInfo returns metadata for this service.
	ServiceInfo() limes.ServiceInfo
	//Resources returns metadata for all the resources that this plugin scrapes
	//from the backend service.
	Resources() []limes.ResourceInfo
	//Scrape queries the backend service for the quota and usage data of all
	//known resources for the given project in the given domain. The string keys
	//in the result map must be identical to the resource names
	//from Resources().
	//
	//The serializedMetrics return value is persisted in the Limes DB and
	//supplied to all subsequent RenderMetrics calls.
	Scrape(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, domainUUID, projectUUID string) (result map[string]ResourceData, serializedMetrics string, error error)
	//SetQuota updates the backend service's quotas for the given project in the
	//given domain to the values specified here. The map is guaranteed to contain
	//values for all resources defined by Resources().
	//
	//An error shall be returned if a value is given in `quotas` for any resource
	//that is ExternallyManaged.
	SetQuota(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, domainUUID, projectUUID string, quotas map[string]uint64) error
	//Rates returns metadata for all the rates that this plugin scrapes
	//from the backend service.
	Rates() []limes.RateInfo
	//ScrapeRates queries the backend service for the usage data of all the rates
	//enumerated by Rates() for the given project in the given domain. The string
	//keys in the result map must be identical to the rate names from Rates().
	//
	//The serializedState return value is persisted in the Limes DB and returned
	//back to the next ScrapeRates() call for the same project in the
	//prevSerializedState argument. Besides that, this field is not interpreted
	//by the core application in any way. The plugin implementation can use this
	//field to carry state between ScrapeRates() calls, esp. to detect and handle
	//counter resets in the backend.
	ScrapeRates(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, domainUUID, projectUUID string, prevSerializedState string) (result map[string]*big.Int, serializedState string, err error)

	//DescribeMetrics is called when Prometheus is scraping metrics from
	//limes-collect, to provide an opportunity to the plugin to emit its own
	//metrics.
	//
	//Together with CollectMetrics, this interface is roughly analogous to the
	//prometheus.Collector interface; cf. documentation over there.
	DescribeMetrics(ch chan<- *prometheus.Desc)
	//CollectMetrics is called when Prometheus is scraping metrics from
	//limes-collect, to provide an opportunity to the plugin to emit its own
	//metrics. The serializedMetrics argument contains the respective value
	//returned from the last Scrape call on the same project.
	//
	//The clusterID should be given as a label to all emitted metrics.
	//
	//Some plugins also emit metrics directly within Scrape. This newer interface
	//should be preferred since metrics emitted here won't be lost between
	//restarts of limes-collect.
	CollectMetrics(ch chan<- prometheus.Metric, clusterID, domainUUID, projectUUID, serializedMetrics string) error
}

//CapacityData contains the total and per-availability-zone capacity data for a
//single resource.
//
//The Subcapacities field may optionally be populated with subcapacities, if the
//capacity plugin providing this CapacityData instance has been instructed to (and
//is able to) scrape subcapacities for this resource.
type CapacityData struct {
	Capacity      uint64
	CapacityPerAZ map[string]*CapacityDataForAZ
	Subcapacities []interface{}
}

//CapacityDataForAZ is the capacity data for a single resource in a single AZ.
type CapacityDataForAZ struct {
	Capacity uint64
	Usage    uint64
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
	//Init is guaranteed to be called before all other methods exposed by the
	//interface.
	Init(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error
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
	//The serializedMetrics return value is persisted in the Limes DB and
	//supplied to all subsequent RenderMetrics calls.
	Scrape(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (result map[string]map[string]CapacityData, serializedMetrics string, err error)

	//DescribeMetrics is called when Prometheus is scraping metrics from
	//limes-collect, to provide an opportunity to the plugin to emit its own
	//metrics.
	//
	//Together with CollectMetrics, this interface is roughly analogous to the
	//prometheus.Collector interface; cf. documentation over there.
	DescribeMetrics(ch chan<- *prometheus.Desc)
	//CollectMetrics is called when Prometheus is scraping metrics from
	//limes-collect, to provide an opportunity to the plugin to emit its own
	//metrics. The serializedMetrics argument contains the respective value
	//returned from the last Scrape call on the same project.
	//
	//The clusterID should be given as a label to all emitted metrics.
	//
	//Some plugins also emit metrics directly within Scrape. This newer interface
	//should be preferred since metrics emitted here won't be lost between
	//restarts of limes-collect.
	CollectMetrics(ch chan<- prometheus.Metric, clusterID, serializedMetrics string) error
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
