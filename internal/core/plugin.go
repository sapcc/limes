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
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/pluggable"

	"github.com/sapcc/limes/internal/db"
)

// KeystoneDomain describes the basic attributes of a Keystone domain.
type KeystoneDomain struct {
	UUID string `json:"id" yaml:"id"`
	Name string `json:"name" yaml:"name"`
}

// KeystoneDomainFromDB converts a db.Domain into a KeystoneDomain.
func KeystoneDomainFromDB(dbDomain db.Domain) KeystoneDomain {
	return KeystoneDomain{
		UUID: dbDomain.UUID,
		Name: dbDomain.Name,
	}
}

// KeystoneProject describes the basic attributes of a Keystone project.
type KeystoneProject struct {
	UUID       string         `json:"id" yaml:"id"`
	Name       string         `json:"name" yaml:"name"`
	ParentUUID string         `json:"parent_id,omitempty" yaml:"parent_id,omitempty"`
	Domain     KeystoneDomain `json:"domain" yaml:"domain"`
}

// KeystoneProjectFromDB converts a db.Project into a KeystoneProject.
func KeystoneProjectFromDB(dbProject db.Project, domain KeystoneDomain) KeystoneProject {
	return KeystoneProject{
		UUID:       dbProject.UUID,
		Name:       dbProject.Name,
		ParentUUID: dbProject.ParentUUID,
		Domain:     domain,
	}
}

// DiscoveryPlugin is the interface that the collector uses to discover Keystone
// projects and domains in a cluster.
type DiscoveryPlugin interface {
	pluggable.Plugin
	//Init is called before any other interface methods, and allows the plugin to
	//perform first-time initialization. If the plugin needs to access OpenStack
	//APIs, it needs to spawn the respective ServiceClients in this method and
	//retain them.
	//
	//Before Init is called, the `discovery.params` provided in the configuration
	//file will be yaml.Unmarshal()ed into the plugin object itself.
	Init(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error
	//ListDomains returns all Keystone domains in the cluster.
	ListDomains() ([]KeystoneDomain, error)
	//ListProjects returns all Keystone projects in the given domain.
	ListProjects(domain KeystoneDomain) ([]KeystoneProject, error)
}

// QuotaPlugin is the interface that the quota/usage collector plugins for all
// backend services must implement. There can only be one QuotaPlugin for each
// backend service.
type QuotaPlugin interface {
	pluggable.Plugin
	//Init is called before any other interface methods, and allows the plugin to
	//perform first-time initialization. If the plugin needs to access OpenStack
	//APIs, it needs to spawn the respective ServiceClients in this method and
	//retain them.
	//
	//Implementations can use it f.i. to discover the available Resources(). For
	//plugins that support subresource scraping, the final argument indicates
	//which resources to scrape (the keys are resource names).
	//
	//Before Init is called, the `services[].params` provided in the config
	//file will be yaml.Unmarshal()ed into the plugin object itself.
	Init(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, scrapeSubresources map[string]bool) error

	//ServiceInfo returns metadata for this service.
	//
	//This receives the `serviceType` as an argument because it needs to appear
	//in the ServiceInfo struct. But in general, a plugin cannot know which
	//serviceType it was instantiated for (esp. in unit tests, where the generic
	//test plugin is instantiated multiple times for different service types).
	ServiceInfo(serviceType string) limes.ServiceInfo

	//Resources returns metadata for all the resources that this plugin scrapes
	//from the backend service.
	Resources() []limesresources.ResourceInfo
	//Scrape queries the backend service for the quota and usage data of all
	//known resources for the given project in the given domain. The string keys
	//in the result map must be identical to the resource names
	//from Resources().
	//
	//The serializedMetrics return value is persisted in the Limes DB and
	//supplied to all subsequent RenderMetrics calls.
	Scrape(project KeystoneProject) (result map[string]ResourceData, serializedMetrics string, error error)
	//IsQuotaAcceptableForProject checks if the given quota set is acceptable
	//for the given project, and returns nil if the quota is acceptable, or a
	//human-readable error otherwise. This should only be used when the
	//acceptability of a specific quota value is tied to the project identity, or
	//to the quotas of other services. (For example, Keppel uses Swift as a
	//storage backend, so nonzero Keppel quota requires nonzero Swift quota in
	//Limes, to catch a common misconfiguration that confuses users.)
	//
	//The `fullQuotas` map uses service type strings as the first key. Since
	//quota plugins can be instantiated under arbitrary service types,
	//implementations should inspect `allServiceInfos` to locate the service type
	//names that they are interested in.
	IsQuotaAcceptableForProject(project KeystoneProject, fullQuotas map[string]map[string]uint64, allServiceInfos []limes.ServiceInfo) error
	//SetQuota updates the backend service's quotas for the given project in the
	//given domain to the values specified here. The map is guaranteed to contain
	//values for all resources defined by Resources().
	SetQuota(project KeystoneProject, quotas map[string]uint64) error

	//Rates returns metadata for all the rates that this plugin scrapes
	//from the backend service.
	Rates() []limesrates.RateInfo
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
	ScrapeRates(project KeystoneProject, prevSerializedState string) (result map[string]*big.Int, serializedState string, err error)

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
	//Some plugins also emit metrics directly within Scrape. This newer interface
	//should be preferred since metrics emitted here won't be lost between
	//restarts of limes-collect.
	CollectMetrics(ch chan<- prometheus.Metric, project KeystoneProject, serializedMetrics string) error
}

// CapacityPlugin is the interface that all capacity collector plugins must
// implement.
//
// While there can only be one QuotaPlugin for each backend service, there may
// be different CapacityPlugin instances for each backend service, and a single
// CapacityPlugin can even report capacities for multiple service types. The
// reason is that quotas are handled in the concrete backend service, thus their
// handling is independent from the underlying infrastructure. Capacity
// calculations, however, may be highly dependent on the infrastructure. For
// example, for the Compute service, there could be different capacity plugins
// for each type of hypervisor (KVM, VMware, etc.) which use the concrete APIs
// of these hypervisors instead of the OpenStack Compute API.
type CapacityPlugin interface {
	pluggable.Plugin
	//Init is called before any other interface methods, and allows the plugin to
	//perform first-time initialization. If the plugin needs to access OpenStack
	//APIs, it needs to spawn the respective ServiceClients in this method and
	//retain them.
	//
	//Before Init is called, the `capacitors[].params` provided in the config
	//file will be yaml.Unmarshal()ed into the plugin object itself.
	Init(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, scrapeSubcapacities map[string]map[string]bool) error
	//Scrape queries the backend service(s) for the capacities of the resources
	//that this plugin is concerned with. The result is a two-dimensional map,
	//with the first key being the service type, and the second key being the
	//resource name. The capacity collector will ignore service types for which
	//there is no QuotaPlugin, and resources which are not advertised by that
	//QuotaPlugin.
	//
	//The serializedMetrics return value is persisted in the Limes DB and
	//supplied to all subsequent RenderMetrics calls.
	Scrape() (result map[string]map[string]PerAZ[CapacityData], serializedMetrics string, err error)

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
	//Some plugins also emit metrics directly within Scrape. This newer interface
	//should be preferred since metrics emitted here won't be lost between
	//restarts of limes-collect.
	CollectMetrics(ch chan<- prometheus.Metric, serializedMetrics string) error
}

var (
	// DiscoveryPluginRegistry is a pluggable.Registry for DiscoveryPlugin implementations.
	DiscoveryPluginRegistry pluggable.Registry[DiscoveryPlugin]
	// QuotaPluginRegistry is a pluggable.Registry for QuotaPlugin implementations.
	QuotaPluginRegistry pluggable.Registry[QuotaPlugin]
	// CapacityPluginRegistry is a pluggable.Registry for CapacityPlugin implementations.
	CapacityPluginRegistry pluggable.Registry[CapacityPlugin]
)
