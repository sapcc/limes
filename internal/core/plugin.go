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
	"context"
	"errors"
	"math/big"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	"github.com/sapcc/go-api-declarations/liquid"
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

// ForLiquid casts this KeystoneProject into the format used in LIQUID requests.
func (p KeystoneProject) ForLiquid() *liquid.ProjectMetadata {
	return &liquid.ProjectMetadata{
		UUID: p.UUID,
		Name: p.Name,
		Domain: liquid.DomainMetadata{
			UUID: p.Domain.UUID,
			Name: p.Domain.Name,
		},
	}
}

// DiscoveryPlugin is the interface that the collector uses to discover Keystone
// projects and domains in a cluster.
type DiscoveryPlugin interface {
	pluggable.Plugin
	// Init is called before any other interface methods, and allows the plugin to
	// perform first-time initialization. If the plugin needs to access OpenStack
	// APIs, it needs to spawn the respective ServiceClients in this method and
	// retain them.
	//
	// Before Init is called, the `discovery.params` provided in the configuration
	// file will be yaml.Unmarshal()ed into the plugin object itself.
	Init(ctx context.Context, client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error
	// ListDomains returns all Keystone domains in the cluster.
	ListDomains(ctx context.Context) ([]KeystoneDomain, error)
	// ListProjects returns all Keystone projects in the given domain.
	ListProjects(ctx context.Context, domain KeystoneDomain) ([]KeystoneProject, error)
}

// QuotaPlugin is the interface that the quota/usage collector plugins for all
// backend services must implement. There can only be one QuotaPlugin for each
// backend service.
type QuotaPlugin interface {
	pluggable.Plugin
	// Init is called before any other interface methods, and allows the plugin to
	// perform first-time initialization. If the plugin needs to access OpenStack
	// APIs, it needs to spawn the respective ServiceClients in this method and
	// retain them.
	//
	// Implementations can use it f.i. to discover the available Resources(). For
	// plugins that support subresource scraping, the final argument indicates
	// which resources to scrape (the keys are resource names).
	//
	// Before Init is called, the `services[].params` provided in the config
	// file will be yaml.Unmarshal()ed into the plugin object itself.
	Init(ctx context.Context, client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, serviceType db.ServiceType) error

	// ServiceInfo returns metadata for this service.
	ServiceInfo() ServiceInfo

	// Resources returns metadata for all the resources that this plugin scrapes
	// from the backend service.
	Resources() map[liquid.ResourceName]liquid.ResourceInfo
	// Scrape queries the backend service for the quota and usage data of all
	// known resources for the given project in the given domain. The string keys
	// in the result map must be identical to the resource names
	// from Resources().
	//
	// The `allAZs` list comes from the Limes config and should be used when
	// building AZ-aware usage data, to ensure that each AZ-aware resource reports
	// usage in all available AZs, even when the project in question does not have
	// usage in every AZ.
	//
	// The `serializedMetrics` return value is persisted in the Limes DB and
	// supplied to all subsequent RenderMetrics calls.
	Scrape(ctx context.Context, project KeystoneProject, allAZs []limes.AvailabilityZone) (result map[liquid.ResourceName]ResourceData, serializedMetrics []byte, err error)

	// BuildServiceUsageRequest generates the request body payload for querying
	// the LIQUID API endpoint /v1/projects/:uuid/report-usage
	BuildServiceUsageRequest(project KeystoneProject, allAZs []limes.AvailabilityZone) (liquid.ServiceUsageRequest, error)

	// SetQuota updates the backend service's quotas for the given project in the
	// given domain to the values specified here. The map is guaranteed to contain
	// values for all resources defined by Resources().
	SetQuota(ctx context.Context, project KeystoneProject, quotas map[liquid.ResourceName]uint64) error

	// Rates returns metadata for all the rates that this plugin scrapes
	// from the backend service.
	Rates() map[liquid.RateName]liquid.RateInfo
	// ScrapeRates queries the backend service for the usage data of all the rates
	// enumerated by Rates() for the given project in the given domain. The string
	// keys in the result map must be identical to the rate names from Rates().
	//
	// The `allAZs` list comes from the Limes config and should be used when
	// building AZ-aware usage data, to ensure that each AZ-aware resource reports
	// usage in all available AZs, even when the project in question does not have
	// usage in every AZ.
	//
	// The serializedState return value is persisted in the Limes DB and returned
	// back to the next ScrapeRates() call for the same project in the
	// prevSerializedState argument. Besides that, this field is not interpreted
	// by the core application in any way. The plugin implementation can use this
	// field to carry state between ScrapeRates() calls, esp. to detect and handle
	// counter resets in the backend.
	ScrapeRates(ctx context.Context, project KeystoneProject, allAZs []limes.AvailabilityZone, prevSerializedState string) (result map[liquid.RateName]*big.Int, serializedState string, err error)

	// DescribeMetrics is called when Prometheus is scraping metrics from
	// limes-collect, to provide an opportunity to the plugin to emit its own
	// metrics.
	//
	// Together with CollectMetrics, this interface is roughly analogous to the
	// prometheus.Collector interface; cf. documentation over there.
	DescribeMetrics(ch chan<- *prometheus.Desc)
	// CollectMetrics is called when Prometheus is scraping metrics from
	// limes-collect, to provide an opportunity to the plugin to emit its own
	// metrics. The serializedMetrics argument contains the respective value
	// returned from the last Scrape call on the same project.
	//
	// Some plugins also emit metrics directly within Scrape. This newer interface
	// should be preferred since metrics emitted here won't be lost between
	// restarts of limes-collect.
	CollectMetrics(ch chan<- prometheus.Metric, project KeystoneProject, serializedMetrics []byte) error
}

// ServiceInfo is a reduced version of type limes.ServiceInfo, suitable for
// being returned from func QuotaPlugin.ServiceInfo().
type ServiceInfo struct {
	ProductName string
	Area        string
}

// ForAPI inflates the given core.ServiceInfo into a limes.ServiceInfo.
// The given ServiceType should be the one that we want to appear in the API.
func (s ServiceInfo) ForAPI(serviceType limes.ServiceType) limes.ServiceInfo {
	return limes.ServiceInfo{
		Type:        serviceType,
		ProductName: s.ProductName,
		Area:        s.Area,
	}
}

// BuildAPIRateInfo converts a RateInfo from LIQUID into the API format.
func BuildAPIRateInfo(rateName limesrates.RateName, rateInfo liquid.RateInfo) limesrates.RateInfo {
	return limesrates.RateInfo{
		Name: rateName,
		Unit: rateInfo.Unit,
	}
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
	// Init is called before any other interface methods, and allows the plugin to
	// perform first-time initialization. If the plugin needs to access OpenStack
	// APIs, it needs to spawn the respective ServiceClients in this method and
	// retain them.
	//
	// Before Init is called, the `capacitors[].params` provided in the config
	// file will be yaml.Unmarshal()ed into the plugin object itself.
	Init(ctx context.Context, client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error
	// Scrape queries the backend service(s) for the capacities of the resources
	// that this plugin is concerned with. The result is a two-dimensional map,
	// with the first key being the service type, and the second key being the
	// resource name. The capacity collector will ignore service types for which
	// there is no QuotaPlugin, and resources which are not advertised by that
	// QuotaPlugin.
	//
	// The serializedMetrics return value is persisted in the Limes DB and
	// supplied to all subsequent RenderMetrics calls.
	Scrape(ctx context.Context, backchannel CapacityPluginBackchannel, allAZs []limes.AvailabilityZone) (result map[db.ServiceType]map[liquid.ResourceName]PerAZ[CapacityData], serializedMetrics []byte, err error)

	// BuildServiceCapacityRequest generates the request body payload for querying
	// the LIQUID API endpoint /v1/report-capacity
	BuildServiceCapacityRequest(backchannel CapacityPluginBackchannel, allAZs []limes.AvailabilityZone) (liquid.ServiceCapacityRequest, error)

	// DescribeMetrics is called when Prometheus is scraping metrics from
	// limes-collect, to provide an opportunity to the plugin to emit its own
	// metrics.
	//
	// Together with CollectMetrics, this interface is roughly analogous to the
	// prometheus.Collector interface; cf. documentation over there.
	DescribeMetrics(ch chan<- *prometheus.Desc)
	// CollectMetrics is called when Prometheus is scraping metrics from
	// limes-collect, to provide an opportunity to the plugin to emit its own
	// metrics. The serializedMetrics argument contains the respective value
	// returned from the last Scrape call on the same project.
	//
	// Some plugins also emit metrics directly within Scrape. This newer interface
	// should be preferred since metrics emitted here won't be lost between
	// restarts of limes-collect.
	CollectMetrics(ch chan<- prometheus.Metric, serializedMetrics []byte, capacitorID string) error
}

// CapacityPluginBackchannel is a callback interface that is provided to
// CapacityPlugin.Scrape(). Most capacity scrape implementations will not need
// this, but some esoteric usecases use this information to distribute
// available capacity among resources in accordance with customer demand.
//
// Note that ResourceDemand is measured against effective capacity, which
// differs from the raw capacity seen by the CapacityPlugin by this
// OvercommitFactor.
type CapacityPluginBackchannel interface {
	GetResourceDemand(serviceType db.ServiceType, resourceName liquid.ResourceName) (liquid.ResourceDemand, error)
}

var (
	// DiscoveryPluginRegistry is a pluggable.Registry for DiscoveryPlugin implementations.
	DiscoveryPluginRegistry pluggable.Registry[DiscoveryPlugin]
	// QuotaPluginRegistry is a pluggable.Registry for QuotaPlugin implementations.
	QuotaPluginRegistry pluggable.Registry[QuotaPlugin]
	// CapacityPluginRegistry is a pluggable.Registry for CapacityPlugin implementations.
	CapacityPluginRegistry pluggable.Registry[CapacityPlugin]
)

// ErrNotALiquid is a custom eror that is thrown by plugins that do not support the LIQUID API
var ErrNotALiquid = errors.New("this plugin is not a liquid")
