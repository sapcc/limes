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
	"encoding/json"

	"github.com/gophercloud/gophercloud"
	"github.com/sapcc/limes/pkg/limes"
)

type capacityCinderPlugin struct {
	cfg limes.CapacitorConfiguration
}

type Float64OrUnknown float64

func (f *Float64OrUnknown) UnmarshalJSON(json_buffer []byte) error {
	if json_buffer[0] == '"' {
		*f = 0
		return nil
	}
	var x float64
	err := json.Unmarshal(json_buffer, &x)
	*f = Float64OrUnknown(x)
	return err
}

func init() {
	limes.RegisterCapacityPlugin(func(c limes.CapacitorConfiguration) limes.CapacityPlugin {
		return &capacityCinderPlugin{c}
	})
}

func (p *capacityCinderPlugin) ID() string{
	return "capacityCinderPlugin"
}

//Scrape implements the limes.CapacityPlugin interface.
func (p *capacityCinderPlugin) Scrape(driver limes.Driver) (map[string]map[string]uint64, error) {
	client, err := p.Client(driver)
	if err != nil {
		return nil, err
	}

	var result gophercloud.Result

	//Get absolute limits for a tenant
	url := client.ServiceURL("scheduler-stats", "get_pools") + "?detail=True"
	_, err = client.Get(url, &result.Body, nil)
	if err != nil {
		return nil, err
	}
	var limit_data struct {
		Pools []struct {
			Name string `json:"name"`
			Capabilities struct {
				TotalCapacity Float64OrUnknown `json:"total_capacity_gb"`
			} `json:"capabilities"`
		} `json:"pools"`
	}
	err = result.ExtractInto(&limit_data)
	if err != nil {
		return nil, err
	}

	//Get availability zones
	url = client.ServiceURL("os-availability-zone")
	_, err = client.Get(url, &result.Body, nil)
	if err != nil {
		return nil, err
	}
	var availability_zone_data struct {
		AvailabilityZoneInfo []struct {
			ZoneName string `json:"zoneName""`
			ZoneState struct {
				available bool `json:"available"`
			} `json:"zoneState"`
		} `json:"availabilityZoneInfo"`
	}
	err = result.ExtractInto(&availability_zone_data)
	if err != nil {
		return nil, err
	}

	return map[string]map[string]uint64{
		"service": {
			"resource": 0,
		},
	},nil

}

