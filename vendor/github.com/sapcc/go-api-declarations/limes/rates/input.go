/*******************************************************************************
*
* Copyright 2022 SAP SE
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

package limesrates

import (
	"encoding/json"
	"sort"
)

// RateRequest contains new rate limit values for rates in multiple services.
// The map key is the service type. This type is used to serialize JSON request
// bodies in PUT requests on projects.
type RateRequest map[string]ServiceRequest

// ServiceQuotaRequest contains new rate limit values for rates in a single
// service. The map key is the rate name. This type appears in type RateRequest.
type ServiceRequest map[string]RateLimitRequest

// RateLimitRequest contains new values for a single rate limit.
// It appears in type ServiceRequest.
type RateLimitRequest struct {
	Limit  uint64
	Window Window
}

type pureRateLimitRequest struct {
	Name   string `json:"name"`
	Limit  uint64 `json:"limit"`
	Window Window `json:"window"`
}

type pureServiceRequest struct {
	Type  string                 `json:"type"`
	Rates []pureRateLimitRequest `json:"rates"`
}

// MarshalJSON implements the json.Marshaler interface.
func (r RateRequest) MarshalJSON() ([]byte, error) {
	list := []pureServiceRequest{}
	for srvType, srvReq := range r {
		sReq := pureServiceRequest{
			Type:  srvType,
			Rates: []pureRateLimitRequest{},
		}

		for rateName, rateReq := range srvReq {
			sReq.Rates = append(sReq.Rates, pureRateLimitRequest{
				Name:   rateName,
				Limit:  rateReq.Limit,
				Window: rateReq.Window,
			})
		}

		//ensure test reproducibility
		sort.Slice(sReq.Rates, func(i, j int) bool {
			return sReq.Rates[i].Name < sReq.Rates[j].Name
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
func (r *RateRequest) UnmarshalJSON(input []byte) error {
	var data []pureServiceRequest
	err := json.Unmarshal(input, &data)
	if err != nil {
		return err
	}

	//remove existing content
	for key := range *r {
		delete(*r, key)
	}
	if *r == nil {
		*r = make(RateRequest, len(data))
	}

	//add new content
	for _, sReq := range data {
		srvReq := make(ServiceRequest, len(sReq.Rates))

		for _, rReq := range sReq.Rates {
			srvReq[rReq.Name] = RateLimitRequest{
				Limit:  rReq.Limit,
				Window: rReq.Window,
			}
		}
		(*r)[sReq.Type] = srvReq
	}
	return nil
}
