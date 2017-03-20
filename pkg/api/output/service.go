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

package output

import "github.com/sapcc/limes/pkg/limes"

//Service contains data about a backend service in the format returned by the API.
type Service struct {
	Type      string      `json:"type"`
	Resources []*Resource `json:"resources,keepempty"`
	//project level only
	ScrapedAt int64 `json:"scraped_at,omitempty"`
	//domain and cluster level only
	MinScrapedAt int64 `json:"min_scraped_at,omitempty"`
	MaxScrapedAt int64 `json:"max_scraped_at,omitempty"`
}

//FindResource finds the resource with that name within this service, or
//appends a new resource.
func (srv *Service) FindResource(name string) *Resource {
	for _, res := range srv.Resources {
		if res.Name == name {
			return res
		}
	}
	res := &Resource{
		Name: name,
		Unit: limes.UnitFor(srv.Type, name),
	}
	srv.Resources = append(srv.Resources, res)
	return res
}
