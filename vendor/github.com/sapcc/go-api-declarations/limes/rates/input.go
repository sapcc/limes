// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package limesrates

import (
	"encoding/json"
	"sort"

	"github.com/sapcc/go-api-declarations/limes"
)

// RateRequest contains new rate limit values for rates in multiple services.
// This type is used to serialize JSON request bodies in PUT requests on projects.
type RateRequest map[limes.ServiceType]ServiceRequest

// ServiceQuotaRequest contains new rate limit values for rates in a single
// service. This type appears in type RateRequest.
type ServiceRequest map[RateName]RateLimitRequest

// RateLimitRequest contains new values for a single rate limit.
// It appears in type ServiceRequest.
type RateLimitRequest struct {
	Limit  uint64
	Window Window
}

type pureRateLimitRequest struct {
	Name   RateName `json:"name"`
	Limit  uint64   `json:"limit"`
	Window Window   `json:"window"`
}

type pureServiceRequest struct {
	Type  limes.ServiceType      `json:"type"`
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

		// ensure test reproducibility
		sort.Slice(sReq.Rates, func(i, j int) bool {
			return sReq.Rates[i].Name < sReq.Rates[j].Name
		})
		list = append(list, sReq)
	}

	// ensure test reproducibility
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

	// remove existing content
	for key := range *r {
		delete(*r, key)
	}
	if *r == nil {
		*r = make(RateRequest, len(data))
	}

	// add new content
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
