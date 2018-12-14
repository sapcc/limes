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

package limes

import (
	"encoding/json"
	"sort"
)

//QuotaRequest contains new quota values for resources in multiple services.
//The map key is the service type. This type is used to serialize JSON
//request bodies in PUT requests on domains and projects.
type QuotaRequest map[string]ServiceQuotaRequest

//ServiceQuotaRequest contains new quota values for the resources in a single
//service. The map key is the resource name. This type appears in type
//QuotaRequest.
type ServiceQuotaRequest map[string]ValueWithUnit

//MarshalJSON implements the json.Marshaler interface.
func (r QuotaRequest) MarshalJSON() ([]byte, error) {
	type resourceQuota struct {
		Name  string `json:"name"`
		Quota uint64 `json:"quota"`
		Unit  Unit   `json:"unit"`
	}

	type serviceQuotas struct {
		Type      string          `json:"type"`
		Resources []resourceQuota `json:"resources"`
	}

	list := []serviceQuotas{}
	for t, rqs := range r {
		sqs := serviceQuotas{
			Type:      t,
			Resources: []resourceQuota{},
		}

		for n, r := range rqs {
			sqs.Resources = append(sqs.Resources, resourceQuota{
				Name:  n,
				Quota: r.Value,
				Unit:  r.Unit,
			})
		}

		//ensure test reproducability
		sort.Slice(sqs.Resources, func(i, j int) bool {
			return sqs.Resources[i].Name < sqs.Resources[j].Name
		})

		list = append(list, sqs)
	}

	//ensure test reproducability
	sort.Slice(list, func(i, j int) bool {
		return list[i].Type < list[j].Type
	})

	return json.Marshal(list)
}

//UnmarshalJSON implements the json.Unmarshaler interface.
func (r *QuotaRequest) UnmarshalJSON(input []byte) error {
	var data []struct {
		Type      string `json:"type"`
		Resources []struct {
			Name  string `json:"name"`
			Quota uint64 `json:"quota"`
			Unit  *Unit  `json:"unit"`
		} `json:"resources"`
	}
	err := json.Unmarshal(input, &data)
	if err != nil {
		return err
	}

	//remove existing content
	for key := range *r {
		delete(*r, key)
	}

	//add new content
	for _, srv := range data {
		sr := make(ServiceQuotaRequest, len(srv.Resources))
		for _, res := range srv.Resources {
			unit := UnitUnspecified
			if res.Unit != nil {
				unit = *res.Unit
			}
			sr[res.Name] = ValueWithUnit{
				Value: res.Quota,
				Unit:  unit,
			}
		}
		(*r)[srv.Type] = sr
	}
	return nil
}

//ServiceCapacityRequest contains updated capacity values for some or all
//resources in a single service. This type is used to serialize JSON request
//bodies in PUT requests on clusters.
type ServiceCapacityRequest struct {
	Type      string                    `json:"type"`
	Resources []ResourceCapacityRequest `json:"resources"`
}

//ResourceCapacityRequest contains an updated capacity value for a single resource.
//It appears in type ServiceCapacityRequest.
type ResourceCapacityRequest struct {
	Name     string `json:"name"`
	Capacity int64  `json:"capacity"`
	Unit     *Unit  `json:"unit"`
	Comment  string `json:"comment"`
}
