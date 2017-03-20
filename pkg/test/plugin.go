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

//Plugin is a limes.Plugin implementation for unit tests, registered as the
//service type "unittest".
type Plugin struct {
	StaticResourceData map[string]*limes.ResourceData
	StaticCapacity     map[string]uint64
}

var resources = []limes.ResourceInfo{
	limes.ResourceInfo{
		Name: "things",
		Unit: limes.UnitNone,
	},
	limes.ResourceInfo{
		Name: "capacity",
		Unit: limes.UnitBytes,
	},
}

func init() {
	limes.RegisterPlugin(&Plugin{
		StaticResourceData: map[string]*limes.ResourceData{
			"things":   &limes.ResourceData{Quota: 42, Usage: 23},
			"capacity": &limes.ResourceData{Quota: 100, Usage: 0},
		},
	})
}

//ServiceType implements the limes.Plugin interface.
func (p *Plugin) ServiceType() string {
	return "unittest"
}

//Resources implements the limes.Plugin interface.
func (p *Plugin) Resources() []limes.ResourceInfo {
	return resources
}

//Scrape implements the limes.Plugin interface.
func (p *Plugin) Scrape(driver limes.Driver, domainUUID, projectUUID string) (map[string]limes.ResourceData, error) {
	result := make(map[string]limes.ResourceData)
	for key, val := range p.StaticResourceData {
		result[key] = *val
	}
	return result, nil
}

//SetQuota implements the limes.Plugin interface.
func (p *Plugin) SetQuota(driver limes.Driver, domainUUID, projectUUID string, quotas map[string]uint64) error {
	return errors.New("SetQuota is not implemented for the unittest plugin")
}

//Capacity implements the limes.Plugin interface.
func (p *Plugin) Capacity(driver limes.Driver) (map[string]uint64, error) {
	return p.StaticCapacity, nil
}
