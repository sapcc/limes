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
	"github.com/sapcc/limes/pkg/collector"
	"github.com/sapcc/limes/pkg/limes"
)

type novaPlugin struct{}

var novaResources = []collector.ResourceInfo{
	collector.ResourceInfo{
		Name: "cores",
		Unit: collector.UnitNone,
	},
	collector.ResourceInfo{
		Name: "instances",
		Unit: collector.UnitNone,
	},
	collector.ResourceInfo{
		Name: "ram",
		Unit: collector.UnitMebibytes,
	},
}

func init() {
	collector.RegisterPlugin("compute", &novaPlugin{})
}

//Resources implements the collector.Plugin interface.
func (p *novaPlugin) Resources() []collector.ResourceInfo {
	return novaResources
}

//Scrape implements the collector.Plugin interface.
func (p *novaPlugin) Scrape(driver limes.Driver, domainUUID, projectUUID string) ([]collector.ResourceData, error) {
	quota, err := driver.GetComputeQuota(projectUUID)
	if err != nil {
		return nil, err
	}
	usage, err := driver.GetComputeUsage(projectUUID)
	if err != nil {
		return nil, err
	}

	return []collector.ResourceData{
		collector.ResourceData{
			Name:  "cores",
			Quota: quota.Cores,
			Usage: usage.Cores,
		},
		collector.ResourceData{
			Name:  "instances",
			Quota: quota.Instances,
			Usage: usage.Instances,
		},
		collector.ResourceData{
			Name:  "ram",
			Quota: quota.RAM,
			Usage: usage.RAM,
		},
	}, nil
}

//Capacity implements the collector.Plugin interface.
func (p *novaPlugin) Capacity(driver limes.Driver) (map[string]uint64, error) {
	//TODO implement
	return map[string]uint64{}, nil
}
