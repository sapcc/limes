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

// QuotaRequest contains new quota values for resources in multiple services.
// The map key is the service type. This type is used to serialize JSON
// request bodies in PUT requests on domains and projects.
type QuotaRequest map[string]ServiceQuotaRequest

// ServiceQuotaRequest contains new quota values for the resources or rate limits in a single
// service. This type appears in type QuotaRequest.
type ServiceQuotaRequest struct {
	Resources ResourceQuotaRequest
	Rates     map[string]RateLimitRequest // key = rate name
}

// ResourceQuotaRequest contains new quota values for resources.
// The map key is the resource name.
type ResourceQuotaRequest map[string]ValueWithUnit

// RateLimitRequest contains new values for a single rate limit.
// It appears in type RateLimitRequests.
type RateLimitRequest struct {
	Limit  uint64
	Window Window
}

// MarshalJSON implements the json.Marshaler interface.
func (r QuotaRequest) MarshalJSON() ([]byte, error) {
	type (
		resourceQuota struct {
			Name  string `json:"name"`
			Quota uint64 `json:"quota"`
			Unit  Unit   `json:"unit"`
		}

		rateLimit struct {
			Name   string `json:"name"`
			Limit  uint64 `json:"limit"`
			Window Window `json:"window"`
		}

		serviceQuotas struct {
			Type      string          `json:"type"`
			Resources []resourceQuota `json:"resources"`
			Rates     []rateLimit     `json:"rates"`
		}
	)

	list := []serviceQuotas{}
	for t, rqs := range r {
		sqs := serviceQuotas{
			Type:      t,
			Resources: []resourceQuota{},
			Rates:     []rateLimit{},
		}

		for n, r := range rqs.Resources {
			sqs.Resources = append(sqs.Resources, resourceQuota{
				Name:  n,
				Quota: r.Value,
				Unit:  r.Unit,
			})
		}

		for n, r := range rqs.Rates {
			sqs.Rates = append(sqs.Rates, rateLimit{
				Name:   n,
				Limit:  r.Limit,
				Window: r.Window,
			})
		}

		//ensure test reproducibility
		sort.Slice(sqs.Resources, func(i, j int) bool {
			return sqs.Resources[i].Name < sqs.Resources[j].Name
		})
		sort.Slice(sqs.Rates, func(i, j int) bool {
			return sqs.Rates[i].Name < sqs.Rates[j].Name
		})
		list = append(list, sqs)
	}

	//ensure test reproducibility
	sort.Slice(list, func(i, j int) bool {
		return list[i].Type < list[j].Type
	})

	return json.Marshal(list)
}

// UnmarshalJSON implements the json.Unmarshaler interface.
func (r *QuotaRequest) UnmarshalJSON(input []byte) error {
	var data []struct {
		Type      string `json:"type"`
		Resources []struct {
			Name  string `json:"name"`
			Quota uint64 `json:"quota"`
			Unit  *Unit  `json:"unit"`
		} `json:"resources"`
		Rates []struct {
			Name   string `json:"name"`
			Limit  uint64 `json:"limit"`
			Window Window `json:"window"`
		} `json:"rates"`
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
		sr := ServiceQuotaRequest{
			Resources: make(ResourceQuotaRequest, len(srv.Resources)),
			Rates:     make(map[string]RateLimitRequest, len(srv.Rates)),
		}

		for _, res := range srv.Resources {
			unit := UnitUnspecified
			if res.Unit != nil {
				unit = *res.Unit
			}
			sr.Resources[res.Name] = ValueWithUnit{
				Value: res.Quota,
				Unit:  unit,
			}
		}
		for _, rl := range srv.Rates {
			sr.Rates[rl.Name] = RateLimitRequest{
				Limit:  rl.Limit,
				Window: rl.Window,
			}
		}
		(*r)[srv.Type] = sr
	}
	return nil
}
