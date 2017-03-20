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
	limes.RegisterPlugin(&novaPlugin{})
}

//ServiceType implements the limes.Plugin interface.
func (p *novaPlugin) ServiceType() string {
	return "compute"
}

//Resources implements the limes.Plugin interface.
func (p *novaPlugin) Resources() []limes.ResourceInfo {
	return novaResources
}

//Scrape implements the limes.Plugin interface.
func (p *novaPlugin) Scrape(driver limes.Driver, domainUUID, projectUUID string) (map[string]limes.ResourceData, error) {
	data, err := driver.CheckCompute(projectUUID)
	if err != nil {
		return nil, err
	}

	//TODO: get rid of this conversion step
	return map[string]limes.ResourceData{
		"cores":     data.Cores,
		"instances": data.Instances,
		"ram":       data.RAM,
	}, nil
}

//SetQuota implements the limes.Plugin interface.
func (p *novaPlugin) SetQuota(driver limes.Driver, domainUUID, projectUUID string, quotas map[string]uint64) error {
	return driver.SetComputeQuota(projectUUID, limes.ComputeData{
		Cores:     limes.ResourceData{Quota: int64(quotas["cores"])},
		Instances: limes.ResourceData{Quota: int64(quotas["instances"])},
		RAM:       limes.ResourceData{Quota: int64(quotas["ram"])},
	})
}

//Capacity implements the limes.Plugin interface.
func (p *novaPlugin) Capacity(driver limes.Driver) (map[string]uint64, error) {
	//TODO implement
	return map[string]uint64{}, nil
}
