// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/domains"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/projects"
	"github.com/sapcc/go-api-declarations/liquid"

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
	UUID       liquid.ProjectUUID `json:"id" yaml:"id"`
	Name       string             `json:"name" yaml:"name"`
	ParentUUID string             `json:"parent_id,omitempty" yaml:"parent_id,omitempty"`
	Domain     KeystoneDomain     `json:"domain" yaml:"domain"`
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
		UUID: string(p.UUID),
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
	// Init is called before any other interface methods, and allows the plugin to
	// perform first-time initialization. If the plugin needs to access OpenStack
	// APIs, it needs to spawn the respective ServiceClients in this method and
	// retain them.
	Init(ctx context.Context, client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error
	// ListDomains returns all Keystone domains in the cluster.
	ListDomains(ctx context.Context) ([]KeystoneDomain, error)
	// ListProjects returns all Keystone projects in the given domain.
	ListProjects(ctx context.Context, domain KeystoneDomain) ([]KeystoneProject, error)
}

// NewDiscoveryPlugin instantiates a DiscoveryPlugin implementation based on
// the provided method and parameters.
func NewDiscoveryPlugin(cfg DiscoveryConfiguration) (DiscoveryPlugin, error) {
	if cfg.Method == "list" {
		return &listDiscoveryPlugin{}, nil
	}
	if cfg.Method == "static" {
		return &StaticDiscoveryPlugin{
			Config: cfg.StaticDiscoveryConfiguration,
		}, nil
	}
	return nil, errors.New("no suitable discovery plugin found")
}

type listDiscoveryPlugin struct {
	KeystoneV3 *gophercloud.ServiceClient
}

// Init implements the DiscoveryPlugin interface.
func (p *listDiscoveryPlugin) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	p.KeystoneV3, err = openstack.NewIdentityV3(provider, eo)
	return err
}

// ListDomains implements the DiscoveryPlugin interface.
func (p *listDiscoveryPlugin) ListDomains(ctx context.Context) ([]KeystoneDomain, error) {
	allPages, err := domains.List(p.KeystoneV3, nil).AllPages(ctx)
	if err != nil {
		return nil, err
	}
	allDomains, err := domains.ExtractDomains(allPages)
	if err != nil {
		return nil, err
	}

	var result []KeystoneDomain
	for _, p := range allDomains {
		result = append(result, KeystoneDomain{
			UUID: p.ID,
			Name: p.Name,
		})
	}
	return result, nil
}

// ListProjects implements the DiscoveryPlugin interface.
func (p *listDiscoveryPlugin) ListProjects(ctx context.Context, domain KeystoneDomain) ([]KeystoneProject, error) {
	allPages, err := projects.List(p.KeystoneV3, projects.ListOpts{DomainID: domain.UUID}).AllPages(ctx)
	if err != nil {
		return nil, err
	}
	allProjects, err := projects.ExtractProjects(allPages)
	if err != nil {
		return nil, err
	}

	var result []KeystoneProject
	for _, p := range allProjects {
		result = append(result, KeystoneProject{
			UUID:       liquid.ProjectUUID(p.ID),
			Name:       p.Name,
			ParentUUID: p.ParentID,
			Domain:     domain,
		})
	}
	return result, nil
}

// StaticDiscoveryPlugin is an implementation of DiscoveryPlugin.
//
// This type should not be instantiated directly. It is only exported because tests need to be able to cast into it.
type StaticDiscoveryPlugin struct {
	Config StaticDiscoveryConfiguration
}

// Init implements the DiscoveryPlugin interface.
func (p *StaticDiscoveryPlugin) Init(ctx context.Context, client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	return nil
}

// ListDomains implements the DiscoveryPlugin interface.
func (p *StaticDiscoveryPlugin) ListDomains(ctx context.Context) ([]KeystoneDomain, error) {
	var result []KeystoneDomain
	if len(p.Config.Domains) == 0 {
		return nil, errors.New("no domains configured")
	}
	for _, domain := range p.Config.Domains {
		if domain.UUID == "" {
			return nil, fmt.Errorf("missing ID for preconfigured domain %q", domain.Name)
		}
		if domain.Name == "" {
			return nil, fmt.Errorf("missing name for preconfigured domain %q", domain.UUID)
		}
		result = append(result, KeystoneDomain{
			UUID: domain.UUID,
			Name: domain.Name,
		})
	}
	return result, nil
}

// ListProjects implements the DiscoveryPlugin interface.
func (p *StaticDiscoveryPlugin) ListProjects(ctx context.Context, queryDomain KeystoneDomain) ([]KeystoneProject, error) {
	var result []KeystoneProject
	if len(p.Config.Domains) == 0 {
		return nil, errors.New("no domains configured")
	}
	for _, domain := range p.Config.Domains {
		if domain.UUID == queryDomain.UUID {
			if len(p.Config.Projects[domain.UUID]) == 0 {
				return nil, fmt.Errorf("no projects configured for domain %s", queryDomain.UUID)
			}
			for _, project := range p.Config.Projects[domain.UUID] {
				if project.UUID == "" {
					return nil, fmt.Errorf("missing ID for preconfigured project %q", project.Name)
				}
				if project.Name == "" {
					return nil, fmt.Errorf("missing name for preconfigured project %q", project.UUID)
				}
				if project.ParentUUID == "" {
					return nil, fmt.Errorf("missing parent_id for preconfigured project %q", project.UUID)
				}
				result = append(result, KeystoneProject{
					UUID:       project.UUID,
					Name:       project.Name,
					ParentUUID: project.ParentUUID,
					Domain:     queryDomain,
				})
			}
		}
	}
	return result, nil
}
