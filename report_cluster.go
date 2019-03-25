/*******************************************************************************
*
* Copyright 2017-2018 SAP SE
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

//ClusterReport contains aggregated data about resource usage in a cluster.
//It is returned by GET endpoints for clusters.
type ClusterReport struct {
	ID           string                `json:"id"`
	Services     ClusterServiceReports `json:"services,keepempty"`
	MaxScrapedAt *int64                `json:"max_scraped_at,omitempty"`
	MinScrapedAt *int64                `json:"min_scraped_at,omitempty"`
}

//ClusterServiceReport is a substructure of ClusterReport containing data for
//a single backend service.
type ClusterServiceReport struct {
	ServiceInfo
	Shared       bool                   `json:"shared,omitempty"`
	Resources    ClusterResourceReports `json:"resources,keepempty"`
	MaxScrapedAt *int64                 `json:"max_scraped_at,omitempty"`
	MinScrapedAt *int64                 `json:"min_scraped_at,omitempty"`
}

//ClusterResourceReport is a substructure of ClusterReport containing data for
//a single resource.
type ClusterResourceReport struct {
	ResourceInfo
	Capacity      *uint64    `json:"capacity,omitempty"`
	RawCapacity   *uint64    `json:"raw_capacity,omitempty"`
	Comment       string     `json:"comment,omitempty"`
	DomainsQuota  uint64     `json:"domains_quota,keepempty"`
	Usage         uint64     `json:"usage,keepempty"`
	BurstUsage    uint64     `json:"burst_usage,omitempty"`
	PhysicalUsage *uint64    `json:"physical_usage,omitempty"`
	Subcapacities JSONString `json:"subcapacities,omitempty"`
}

//ClusterServiceReports provides fast lookup of services by service type, but
//serializes to JSON as a list.
type ClusterServiceReports map[string]*ClusterServiceReport

//MarshalJSON implements the json.Marshaler interface.
func (s ClusterServiceReports) MarshalJSON() ([]byte, error) {
	//serialize with ordered keys to ensure testcase stability
	types := make([]string, 0, len(s))
	for typeStr := range s {
		types = append(types, typeStr)
	}
	sort.Strings(types)
	list := make([]*ClusterServiceReport, len(s))
	for idx, typeStr := range types {
		list[idx] = s[typeStr]
	}
	return json.Marshal(list)
}

//UnmarshalJSON implements the json.Unmarshaler interface
func (s *ClusterServiceReports) UnmarshalJSON(b []byte) error {
	tmp := make([]*ClusterServiceReport, 0)
	err := json.Unmarshal(b, &tmp)
	if err != nil {
		return err
	}
	t := make(ClusterServiceReports)
	for _, cs := range tmp {
		t[cs.Type] = cs
	}
	*s = ClusterServiceReports(t)
	return nil
}

//ClusterResourceReports provides fast lookup of resources by resource name,
//but serializes to JSON as a list.
type ClusterResourceReports map[string]*ClusterResourceReport

//MarshalJSON implements the json.Marshaler interface.
func (r ClusterResourceReports) MarshalJSON() ([]byte, error) {
	//serialize with ordered keys to ensure testcase stability
	names := make([]string, 0, len(r))
	for name := range r {
		names = append(names, name)
	}
	sort.Strings(names)
	list := make([]*ClusterResourceReport, len(r))
	for idx, name := range names {
		list[idx] = r[name]
	}
	return json.Marshal(list)
}

//UnmarshalJSON implements the json.Unmarshaler interface
func (r *ClusterResourceReports) UnmarshalJSON(b []byte) error {
	tmp := make([]*ClusterResourceReport, 0)
	err := json.Unmarshal(b, &tmp)
	if err != nil {
		return err
	}
	t := make(ClusterResourceReports)
	for _, cr := range tmp {
		t[cr.Name] = cr
	}
	*r = ClusterResourceReports(t)
	return nil
}
