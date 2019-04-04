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

//ProjectReport contains all data about resource usage in a project.
type ProjectReport struct {
	UUID       string                `json:"id"`
	Name       string                `json:"name"`
	ParentUUID string                `json:"parent_id"`
	Bursting   *ProjectBurstingInfo  `json:"bursting,omitempty"`
	Services   ProjectServiceReports `json:"services,keepempty"`
}

//ProjectBurstingInfo is a substructure of ProjectReport containing information about
//quota bursting. (It is omitted if bursting is not supported for the project's
//cluster.)
type ProjectBurstingInfo struct {
	Enabled    bool               `json:"enabled,keepempty"`
	Multiplier BurstingMultiplier `json:"multiplier,keepempty"`
}

//ProjectServiceReport is a substructure of ProjectReport containing data for
//a single backend service.
type ProjectServiceReport struct {
	ServiceInfo
	Resources ProjectResourceReports `json:"resources,keepempty"`
	ScrapedAt *int64                 `json:"scraped_at,omitempty"`
}

//ProjectResourceReport is a substructure of ProjectReport containing data for
//a single resource.
type ProjectResourceReport struct {
	//Several fields are pointers to values to enable precise control over which fields are rendered in output.
	ResourceInfo
	Quota         uint64           `json:"quota,keepempty"`
	UsableQuota   uint64           `json:"usable_quota,keepempty"`
	Usage         uint64           `json:"usage,keepempty"`
	BurstUsage    uint64           `json:"burst_usage,omitempty"`
	PhysicalUsage *uint64          `json:"physical_usage,omitempty"`
	BackendQuota  *int64           `json:"backend_quota,omitempty"`
	Subresources  JSONString       `json:"subresources,omitempty"`
	Scaling       *ScalingBehavior `json:"scales_with,omitempty"`
	//Annotations may contain arbitrary metadata that was configured for this
	//resource in this scope by Limes' operator.
	Annotations map[string]interface{} `json:"annotations,omitempty"`
}

//ProjectServiceReports provides fast lookup of services using a map, but serializes
//to JSON as a list.
type ProjectServiceReports map[string]*ProjectServiceReport

//MarshalJSON implements the json.Marshaler interface.
func (s ProjectServiceReports) MarshalJSON() ([]byte, error) {
	//serialize with ordered keys to ensure testcase stability
	types := make([]string, 0, len(s))
	for typeStr := range s {
		types = append(types, typeStr)
	}
	sort.Strings(types)
	list := make([]*ProjectServiceReport, len(s))
	for idx, typeStr := range types {
		list[idx] = s[typeStr]
	}
	return json.Marshal(list)
}

//UnmarshalJSON implements the json.Unmarshaler interface
func (s *ProjectServiceReports) UnmarshalJSON(b []byte) error {
	tmp := make([]*ProjectServiceReport, 0)
	err := json.Unmarshal(b, &tmp)
	if err != nil {
		return err
	}
	t := make(ProjectServiceReports)
	for _, ps := range tmp {
		t[ps.Type] = ps
	}
	*s = ProjectServiceReports(t)
	return nil
}

//ProjectResourceReports provides fast lookup of resources using a map, but serializes
//to JSON as a list.
type ProjectResourceReports map[string]*ProjectResourceReport

//MarshalJSON implements the json.Marshaler interface.
func (r ProjectResourceReports) MarshalJSON() ([]byte, error) {
	//serialize with ordered keys to ensure testcase stability
	names := make([]string, 0, len(r))
	for name := range r {
		names = append(names, name)
	}
	sort.Strings(names)
	list := make([]*ProjectResourceReport, len(r))
	for idx, name := range names {
		list[idx] = r[name]
	}
	return json.Marshal(list)
}

//UnmarshalJSON implements the json.Unmarshaler interface
func (r *ProjectResourceReports) UnmarshalJSON(b []byte) error {
	tmp := make([]*ProjectResourceReport, 0)
	err := json.Unmarshal(b, &tmp)
	if err != nil {
		return err
	}
	t := make(ProjectResourceReports)
	for _, pr := range tmp {
		t[pr.Name] = pr
	}
	*r = ProjectResourceReports(t)
	return nil
}
