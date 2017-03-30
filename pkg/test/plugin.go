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

package test

import (
	"errors"

	"github.com/sapcc/limes/pkg/limes"
)

//Plugin is a limes.QuotaPlugin implementation for unit tests, registered as
//the service type "unittest".
type Plugin struct {
	StaticServiceType  string
	StaticResourceData map[string]*limes.ResourceData
	StaticCapacity     map[string]uint64
	OverrideQuota      map[string]map[string]uint64
	//behavior flags that can be set by a unit test
	SetQuotaFails bool
}

var resources = []limes.ResourceInfo{
	{
		Name: "capacity",
		Unit: limes.UnitBytes,
	},
	{
		Name: "things",
		Unit: limes.UnitNone,
	},
}

func init() {
	limes.RegisterQuotaPlugin(NewPluginFactory("unittest"))
}

//NewPluginFactory creates a new Plugin factory for the given service type.
func NewPluginFactory(serviceType string) func(limes.ServiceConfiguration) limes.QuotaPlugin {
	return func(c limes.ServiceConfiguration) limes.QuotaPlugin {
		return &Plugin{
			StaticServiceType: serviceType,
			StaticResourceData: map[string]*limes.ResourceData{
				"things":   {Quota: 42, Usage: 23},
				"capacity": {Quota: 100, Usage: 0},
			},
			OverrideQuota: make(map[string]map[string]uint64),
		}
	}
}

//ServiceType implements the limes.QuotaPlugin interface.
func (p *Plugin) ServiceType() string {
	return p.StaticServiceType
}

//Resources implements the limes.QuotaPlugin interface.
func (p *Plugin) Resources() []limes.ResourceInfo {
	return resources
}

//Scrape implements the limes.QuotaPlugin interface.
func (p *Plugin) Scrape(driver limes.Driver, domainUUID, projectUUID string) (map[string]limes.ResourceData, error) {
	result := make(map[string]limes.ResourceData)
	for key, val := range p.StaticResourceData {
		result[key] = *val
	}

	data, exists := p.OverrideQuota[projectUUID]
	if exists {
		for resourceName, quota := range data {
			result[resourceName] = limes.ResourceData{
				Quota: int64(quota),
				Usage: result[resourceName].Usage,
			}
		}
	}

	return result, nil
}

//SetQuota implements the limes.QuotaPlugin interface.
func (p *Plugin) SetQuota(driver limes.Driver, domainUUID, projectUUID string, quotas map[string]uint64) error {
	if p.SetQuotaFails {
		return errors.New("SetQuota failed as requested")
	}
	p.OverrideQuota[projectUUID] = quotas
	return nil
}
