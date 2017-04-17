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

package api

import (
	"encoding/json"
)

//ServiceQuotas contains new quota values for resources in multiple services.
//The map key is the service type. This type is used to unserialize JSON
//request bodies in PUT requests.
type ServiceQuotas map[string]ResourceQuotas

//ResourceQuotas contains new quota values for the resources in a single
//service. The map key is the resource name. This type is used to unserialize
//JSON request bodies in PUT requests.
type ResourceQuotas map[string]uint64

//UnmarshalJSON implements the json.Unmarshaler interface.
func (sq *ServiceQuotas) UnmarshalJSON(input []byte) error {
	var data []struct {
		Type      string `json:"type"`
		Resources []struct {
			Name  string `json:"name"`
			Quota uint64 `json:"quota"`
		} `json:"resources"`
	}
	err := json.Unmarshal(input, &data)
	if err != nil {
		return err
	}

	//remove existing content
	for key := range *sq {
		delete(*sq, key)
	}

	//add new content
	for _, srv := range data {
		rq := make(ResourceQuotas, len(srv.Resources))
		for _, res := range srv.Resources {
			rq[res.Name] = res.Quota
		}
		(*sq)[srv.Type] = rq
	}
	return nil
}

//ServiceCapacities contains updated capacity values for some or all resources
//in a single service.
type ServiceCapacities struct {
	Type      string             `json:"type"`
	Resources []ResourceCapacity `json:"resources"`
}

//ResourceCapacity contains an updated capacity value for a single resource.
type ResourceCapacity struct {
	Name     string `json:"name"`
	Capacity int64  `json:"capacity"`
	Comment  string `json:"comment"`
}
