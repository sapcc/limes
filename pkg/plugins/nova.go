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
	"github.com/sapcc/limes/pkg/limes"
)

type novaPlugin struct{}

var novaResources = []limes.ResourceInfo{
	limes.ResourceInfo{
		Name: "cores",
		Unit: limes.UnitNone,
	},
	limes.ResourceInfo{
		Name: "instances",
		Unit: limes.UnitNone,
	},
	limes.ResourceInfo{
		Name: "ram",
		Unit: limes.UnitMebibytes,
	},
}

func init() {
	limes.RegisterPlugin("compute", &novaPlugin{})
}

//Resources implements the limes.Plugin interface.
func (p *novaPlugin) Resources() []limes.ResourceInfo {
	return novaResources
}

//Scrape implements the limes.Plugin interface.
func (p *novaPlugin) Scrape(driver limes.Driver, domainUUID, projectUUID string) ([]limes.ResourceData, error) {
	quota, err := driver.GetComputeQuota(projectUUID)
	if err != nil {
		return nil, err
	}
	usage, err := driver.GetComputeUsage(projectUUID)
	if err != nil {
		return nil, err
	}

	return []limes.ResourceData{
		limes.ResourceData{
			Name:  "cores",
			Quota: quota.Cores,
			//FIXME: unsafe sign cast
			Usage: uint64(usage.Cores),
		},
		limes.ResourceData{
			Name:  "instances",
			Quota: quota.Instances,
			//FIXME: unsafe sign cast
			Usage: uint64(usage.Instances),
		},
		limes.ResourceData{
			Name:  "ram",
			Quota: quota.RAM,
			//FIXME: unsafe sign cast
			Usage: uint64(usage.RAM),
		},
	}, nil
}

//Capacity implements the limes.Plugin interface.
func (p *novaPlugin) Capacity(driver limes.Driver) (map[string]uint64, error) {
	//TODO implement
	return map[string]uint64{}, nil
}
