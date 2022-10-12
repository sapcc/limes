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

package limesresources

import (
	"encoding/json"
	"sort"

	"github.com/sapcc/go-api-declarations/limes"
)

// QuotaRequest contains new quota values for resources in multiple services.
// The map key is the service type. This type is used to serialize JSON
// request bodies in PUT requests on domains and projects.
type QuotaRequest map[string]ServiceQuotaRequest

// ServiceQuotaRequest contains new quota values for resources in a single service.
// The map key is the resource name. This type appears in type QuotaRequest.
type ServiceQuotaRequest map[string]ResourceQuotaRequest

// ResourceQuotaRequest contains new quota values for a single resource.
// This type appears in type ServiceQuotaRequest.
type ResourceQuotaRequest limes.ValueWithUnit

type pureResourceQuotaRequest struct {
	Name  string      `json:"name"`
	Quota uint64      `json:"quota"`
	Unit  *limes.Unit `json:"unit"`
}

type pureServiceQuotaRequest struct {
	Type      string                     `json:"type"`
	Resources []pureResourceQuotaRequest `json:"resources"`
}

// MarshalJSON implements the json.Marshaler interface.
func (r QuotaRequest) MarshalJSON() ([]byte, error) {
	list := []pureServiceQuotaRequest{}
	for srvType, srvReq := range r {
		sReq := pureServiceQuotaRequest{
			Type:      srvType,
			Resources: []pureResourceQuotaRequest{},
		}

		for resName, resReq := range srvReq {
			unit := resReq.Unit
			sReq.Resources = append(sReq.Resources, pureResourceQuotaRequest{
				Name:  resName,
				Quota: resReq.Value,
				Unit:  &unit,
			})
		}

		//ensure test reproducibility
		sort.Slice(sReq.Resources, func(i, j int) bool {
			return sReq.Resources[i].Name < sReq.Resources[j].Name
		})
		list = append(list, sReq)
	}

	//ensure test reproducibility
	sort.Slice(list, func(i, j int) bool {
		return list[i].Type < list[j].Type
	})

	return json.Marshal(list)
}

// UnmarshalJSON implements the json.Unmarshaler interface.
func (r *QuotaRequest) UnmarshalJSON(input []byte) error {
	var data []pureServiceQuotaRequest
	err := json.Unmarshal(input, &data)
	if err != nil {
		return err
	}

	//remove existing content
	for key := range *r {
		delete(*r, key)
	}
	if *r == nil {
		*r = make(QuotaRequest, len(data))
	}

	//add new content
	for _, sReq := range data {
		srvReq := make(ServiceQuotaRequest, len(sReq.Resources))

		for _, rReq := range sReq.Resources {
			unit := limes.UnitUnspecified
			if rReq.Unit != nil {
				unit = *rReq.Unit
			}
			srvReq[rReq.Name] = ResourceQuotaRequest{
				Value: rReq.Quota,
				Unit:  unit,
			}
		}
		(*r)[sReq.Type] = srvReq
	}
	return nil
}
