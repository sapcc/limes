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

package plugins

import (
	"errors"
	"fmt"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/identity/v3/projects"
	"github.com/gophercloud/gophercloud/openstack/identity/v3/roles"
	"github.com/gophercloud/gophercloud/pagination"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/limes/pkg/core"
	"github.com/sapcc/limes/pkg/util"
)

type roleAssignmentDiscoveryPlugin struct {
	cfg    core.DiscoveryConfiguration
	lister *listDiscoveryPlugin
}

func init() {
	core.RegisterDiscoveryPlugin(func(c core.DiscoveryConfiguration) core.DiscoveryPlugin {
		//this plugin embeds another plugin to avoid code duplication (see ListDomains())
		return &roleAssignmentDiscoveryPlugin{
			cfg:    c,
			lister: &listDiscoveryPlugin{c},
		}
	})
}

//Method implements the core.DiscoveryPlugin interface.
func (p *roleAssignmentDiscoveryPlugin) Method() string {
	return "role-assignment"
}

//ListDomains implements the core.DiscoveryPlugin interface.
func (p *roleAssignmentDiscoveryPlugin) ListDomains(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) ([]core.KeystoneDomain, error) {
	return p.lister.ListDomains(provider, eo)
}

//ListProjects implements the core.DiscoveryPlugin interface.
func (p *roleAssignmentDiscoveryPlugin) ListProjects(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, domain core.KeystoneDomain) ([]core.KeystoneProject, error) {
	if p.cfg.RoleAssignment.RoleName == "" {
		logg.Fatal(`missing role name for discovery plugin "role-assignment"`)
	}

	client, err := openstack.NewIdentityV3(provider, eo)
	if err != nil {
		return nil, err
	}

	//resolve role name into ID
	var opts roles.ListAssignmentsOpts
	opts.RoleID, err = getRoleIDForName(client, p.cfg.RoleAssignment.RoleName)
	if err != nil {
		return nil, fmt.Errorf(`cannot get role ID for role "%s": %s`,
			p.cfg.RoleAssignment.RoleName, util.ErrorToString(err),
		)
	}

	//list role assignments
	projectIDs := make(map[string]struct{})

	err = roles.ListAssignments(client, opts).EachPage(func(page pagination.Page) (bool, error) {
		assignments, err := roles.ExtractRoleAssignments(page)
		if err != nil {
			return false, err
		}
		for _, assignment := range assignments {
			projectIDs[assignment.Scope.Project.ID] = struct{}{}
		}
		return true, nil
	})
	if err != nil {
		return nil, fmt.Errorf(`cannot list role assignments: %s`, util.ErrorToString(err))
	}

	//filter projects by domain and get name
	var result []core.KeystoneProject
	for projectID := range projectIDs {
		if projectID == "" {
			continue
		}

		project, err := projects.Get(client, projectID).Extract()
		if err != nil {
			return nil, fmt.Errorf(`cannot query project %s: %s`, projectID, util.ErrorToString(err))
		}
		if project.DomainID != domain.UUID {
			continue
		}

		result = append(result, core.KeystoneProject{
			UUID:       project.ID,
			Name:       project.Name,
			ParentUUID: project.ParentID,
			Domain:     domain,
		})
	}

	logg.Debug("domain = %s -> projects = %#v", domain.UUID, result)
	return result, nil
}

func getRoleIDForName(client *gophercloud.ServiceClient, roleName string) (string, error) {
	var opts struct {
		RoleName string `q:"name"`
	}
	opts.RoleName = roleName
	query, err := gophercloud.BuildQueryString(opts)
	if err != nil {
		return "", err
	}

	var result gophercloud.Result
	_, err = client.Get(client.ServiceURL("roles")+query.String(), &result.Body, nil)
	if err != nil {
		return "", err
	}

	var data struct {
		Roles []struct {
			ID string `json:"id"`
		} `json:"roles"`
	}
	err = result.ExtractInto(&data)
	if err == nil && len(data.Roles) == 0 {
		return "", errors.New("no such role")
	}
	return data.Roles[0].ID, err
}
