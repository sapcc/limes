// SPDX-FileCopyrightText: 2021 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package plugins

import (
	"context"
	"errors"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"

	"github.com/sapcc/limes/internal/core"
)

type staticDiscoveryPlugin struct {
	Domains []struct {
		UUID     string `yaml:"id"`
		Name     string `yaml:"name"`
		Projects []struct {
			UUID       string `yaml:"id"`
			Name       string `yaml:"name"`
			ParentUUID string `yaml:"parent_id"`
		} `yaml:"projects"`
	} `yaml:"domains"`
}

func init() {
	core.DiscoveryPluginRegistry.Add(func() core.DiscoveryPlugin { return &staticDiscoveryPlugin{} })
}

// PluginTypeID implements the core.DiscoveryPlugin interface.
func (p *staticDiscoveryPlugin) PluginTypeID() string {
	return "static"
}

// Init implements the core.DiscoveryPlugin interface.
func (p *staticDiscoveryPlugin) Init(ctx context.Context, client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	return nil
}

// ListDomains implements the core.DiscoveryPlugin interface.
func (p *staticDiscoveryPlugin) ListDomains(ctx context.Context) ([]core.KeystoneDomain, error) {
	var result []core.KeystoneDomain
	if len(p.Domains) == 0 {
		return nil, errors.New("no domains configured")
	}
	for _, domain := range p.Domains {
		if domain.UUID == "" {
			return nil, fmt.Errorf("missing ID for preconfigured domain %q", domain.Name)
		}
		if domain.Name == "" {
			return nil, fmt.Errorf("missing name for preconfigured domain %q", domain.UUID)
		}
		result = append(result, core.KeystoneDomain{
			UUID: domain.UUID,
			Name: domain.Name,
		})
	}
	return result, nil
}

// ListProjects implements the core.DiscoveryPlugin interface.
func (p *staticDiscoveryPlugin) ListProjects(ctx context.Context, queryDomain core.KeystoneDomain) ([]core.KeystoneProject, error) {
	var result []core.KeystoneProject
	if len(p.Domains) == 0 {
		return nil, errors.New("no domains configured")
	}
	for _, domain := range p.Domains {
		if domain.UUID == queryDomain.UUID {
			if len(domain.Projects) == 0 {
				return nil, fmt.Errorf("no projects configured for domain %s", queryDomain.UUID)
			}
			for _, project := range domain.Projects {
				if project.UUID == "" {
					return nil, fmt.Errorf("missing ID for preconfigured project %q", project.Name)
				}
				if project.Name == "" {
					return nil, fmt.Errorf("missing name for preconfigured project %q", project.UUID)
				}
				if project.ParentUUID == "" {
					return nil, fmt.Errorf("missing parent_id for preconfigured project %q", project.UUID)
				}
				result = append(result, core.KeystoneProject{
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
