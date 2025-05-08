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
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/liquidapi"
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
func (p KeystoneProject) ForLiquid() liquid.ProjectMetadata {
	return liquid.ProjectMetadata{
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

var (
	// DiscoveryPluginRegistry is a pluggable.Registry for DiscoveryPlugin implementations.
	DiscoveryPluginRegistry pluggable.Registry[DiscoveryPlugin]
)

// LiquidClient is a wrapper for liquidapi.Client
// Allows for the implementation of a mock client that is used in unit tests
type LiquidClient interface {
	GetInfo(ctx context.Context) (result liquid.ServiceInfo, err error)
	GetCapacityReport(ctx context.Context, req liquid.ServiceCapacityRequest) (result liquid.ServiceCapacityReport, err error)
	GetUsageReport(ctx context.Context, projectUUID string, req liquid.ServiceUsageRequest) (result liquid.ServiceUsageReport, err error)
	PutQuota(ctx context.Context, projectUUID string, req liquid.ServiceQuotaRequest) (err error)
}

// NewLiquidClient is usually a synonym for liquidapi.NewClient().
// In tests, it serves as a dependency injection slot to allow type Cluster to
// access mock liquids prepared by the test's specific setup code.
var NewLiquidClient = func(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, opts liquidapi.ClientOpts) (LiquidClient, error) {
	client, err := liquidapi.NewClient(provider, eo, opts)
	if err != nil {
		return nil, fmt.Errorf("cannot initialize ServiceClient for %s: %w", opts.ServiceType, err)
	}
	return client, nil
}
