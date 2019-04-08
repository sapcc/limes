/*******************************************************************************
*
* Copyright 2018 SAP SE
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

//DomainReport contains aggregated data about resource usage in a domain.
//It is returned by GET requests on domains.
type DomainReport struct {
	UUID     string               `json:"id"`
	Name     string               `json:"name"`
	Services DomainServiceReports `json:"services,keepempty"`
}

//DomainServiceReport is a substructure of DomainReport containing data for
//a single backend service.
type DomainServiceReport struct {
	ServiceInfo
	Resources    DomainResourceReports `json:"resources,keepempty"`
	MaxScrapedAt *int64                `json:"max_scraped_at,omitempty"`
	MinScrapedAt *int64                `json:"min_scraped_at,omitempty"`
}

//DomainResourceReport is a substructure of DomainReport containing data for
//a single resource.
type DomainResourceReport struct {
	ResourceInfo
	DomainQuota   uint64 `json:"quota,keepempty"`
	ProjectsQuota uint64 `json:"projects_quota,keepempty"`
	Usage         uint64 `json:"usage,keepempty"`
	BurstUsage    uint64 `json:"burst_usage,omitempty"`
	//These are pointers to values to enable precise control over whether this field is rendered in output.
	PhysicalUsage        *uint64          `json:"physical_usage,omitempty"`
	BackendQuota         *uint64          `json:"backend_quota,omitempty"`
	InfiniteBackendQuota *bool            `json:"infinite_backend_quota,omitempty"`
	Scaling              *ScalingBehavior `json:"scales_with,omitempty"`
	//Annotations may contain arbitrary metadata that was configured for this
	//resource in this scope by Limes' operator.
	Annotations map[string]interface{} `json:"annotations,omitempty"`
}

//DomainServiceReports provides fast lookup of services using a map, but serializes
//to JSON as a list.
type DomainServiceReports map[string]*DomainServiceReport

//MarshalJSON implements the json.Marshaler interface.
func (s DomainServiceReports) MarshalJSON() ([]byte, error) {
	//serialize with ordered keys to ensure testcase stability
	types := make([]string, 0, len(s))
	for typeStr := range s {
		types = append(types, typeStr)
	}
	sort.Strings(types)
	list := make([]*DomainServiceReport, len(s))
	for idx, typeStr := range types {
		list[idx] = s[typeStr]
	}
	return json.Marshal(list)
}

//UnmarshalJSON implements the json.Unmarshaler interface
func (s *DomainServiceReports) UnmarshalJSON(b []byte) error {
	tmp := make([]*DomainServiceReport, 0)
	err := json.Unmarshal(b, &tmp)
	if err != nil {
		return err
	}
	t := make(DomainServiceReports)
	for _, ds := range tmp {
		t[ds.Type] = ds
	}
	*s = DomainServiceReports(t)
	return nil
}

//DomainResourceReports provides fast lookup of resources using a map, but serializes
//to JSON as a list.
type DomainResourceReports map[string]*DomainResourceReport

//MarshalJSON implements the json.Marshaler interface.
func (r DomainResourceReports) MarshalJSON() ([]byte, error) {
	//serialize with ordered keys to ensure testcase stability
	names := make([]string, 0, len(r))
	for name := range r {
		names = append(names, name)
	}
	sort.Strings(names)
	list := make([]*DomainResourceReport, len(r))
	for idx, name := range names {
		list[idx] = r[name]
	}
	return json.Marshal(list)
}

//UnmarshalJSON implements the json.Unmarshaler interface
func (r *DomainResourceReports) UnmarshalJSON(b []byte) error {
	tmp := make([]*DomainResourceReport, 0)
	err := json.Unmarshal(b, &tmp)
	if err != nil {
		return err
	}
	t := make(DomainResourceReports)
	for _, dr := range tmp {
		t[dr.Name] = dr
	}
	*r = DomainResourceReports(t)
	return nil
}
