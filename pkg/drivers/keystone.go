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

package drivers

import (
	policy "github.com/databus23/goslo.policy"
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/identity/v3/tokens"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/util"
)

func (d realDriver) keystoneClient() (*gophercloud.ServiceClient, error) {
	return openstack.NewIdentityV3(d.Client,
		gophercloud.EndpointOpts{Availability: gophercloud.AvailabilityPublic},
	)
}

//ListDomains implements the limes.Driver interface.
func (d realDriver) ListDomains() ([]limes.KeystoneDomain, error) {
	client, err := d.keystoneClient()
	if err != nil {
		return nil, err
	}

	//gophercloud does not support domain listing yet - do it manually
	url := client.ServiceURL("domains")
	var result gophercloud.Result
	_, err = client.Get(url, &result.Body, nil)
	if err != nil {
		return nil, err
	}

	var data struct {
		Domains []limes.KeystoneDomain `json:"domains"`
	}
	err = result.ExtractInto(&data)
	return data.Domains, err
}

//ListProjects implements the limes.Driver interface.
func (d realDriver) ListProjects(domainUUID string) ([]limes.KeystoneProject, error) {
	client, err := d.keystoneClient()
	if err != nil {
		return nil, err
	}

	//gophercloud does not support project listing yet - do it manually
	url := client.ServiceURL("projects")
	var opts struct {
		DomainUUID string `q:"domain_id"`
	}
	opts.DomainUUID = domainUUID
	query, err := gophercloud.BuildQueryString(opts)
	if err != nil {
		return nil, err
	}
	url += query.String()

	var result gophercloud.Result
	_, err = client.Get(url, &result.Body, nil)
	if err != nil {
		return nil, err
	}

	var data struct {
		Projects []limes.KeystoneProject `json:"projects"`
	}
	err = result.ExtractInto(&data)
	return data.Projects, err
}

//CheckUserPermission implements the limes.Driver interface.
func (d realDriver) CheckUserPermission(token, rule string, enforcer *policy.Enforcer, requestParams map[string]string) (bool, error) {
	client, err := d.keystoneClient()
	if err != nil {
		return false, err
	}

	response := tokens.Get(client, token)
	if response.Err != nil {
		//this includes 4xx responses, so after this point, we can be sure that the token is valid
		return false, response.Err
	}

	//use a custom token struct instead of tokens.Token which is way incomplete
	var tokenData keystoneToken
	err = response.ExtractInto(&tokenData)
	if err != nil {
		return false, err
	}

	//map token into policy.Context
	util.LogDebug("enforcer = %#v", enforcer)
	util.LogDebug("token = %#v", tokenData)
	util.LogDebug("context = %#v", tokenData.ToContext(requestParams))
	util.LogDebug("rule = %s", rule)
	return enforcer.Enforce(rule, tokenData.ToContext(requestParams)), nil
}

type keystoneToken struct {
	DomainScope  keystoneTokenThing         `json:"domain"`
	ProjectScope keystoneTokenThingInDomain `json:"project"`
	Roles        []keystoneTokenThing       `json:"roles"`
	User         keystoneTokenThingInDomain `json:"user"`
}

type keystoneTokenThing struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type keystoneTokenThingInDomain struct {
	keystoneTokenThing
	Domain keystoneTokenThing `json:"domain"`
}

func (t *keystoneToken) ToContext(requestParams map[string]string) policy.Context {
	c := policy.Context{
		Roles: make([]string, 0, len(t.Roles)),
		Auth: map[string]string{
			"user_id":             t.User.ID,
			"user_name":           t.User.Name,
			"user_domain_id":      t.User.Domain.ID,
			"user_domain_name":    t.User.Domain.Name,
			"domain_id":           t.DomainScope.ID,
			"domain_name":         t.DomainScope.Name,
			"project_id":          t.ProjectScope.ID,
			"project_name":        t.ProjectScope.Name,
			"project_domain_id":   t.ProjectScope.Domain.ID,
			"project_domain_name": t.ProjectScope.Domain.Name,
			"tenant_id":           t.ProjectScope.ID,
			"tenant_name":         t.ProjectScope.Name,
			"tenant_domain_id":    t.ProjectScope.Domain.ID,
			"tenant_domain_name":  t.ProjectScope.Domain.Name,
		},
		Request: requestParams,
		Logger:  util.LogDebug,
	}
	for key, value := range c.Auth {
		if value == "" {
			delete(c.Auth, key)
		}
	}
	for _, role := range t.Roles {
		c.Roles = append(c.Roles, role.Name)
	}
	if c.Request == nil {
		c.Request = map[string]string{}
	}

	return c
}
