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

package reports

import (
	"encoding/json"
	"errors"
	"sort"

	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/limes"
)

//Cluster contains aggregated data about resource usage in a cluster.
type Cluster struct {
	UUID         string          `json:"id"`
	Services     ClusterServices `json:"services,keepempty"`
	MaxScrapedAt int64           `json:"max_scraped_at,keepempty"`
	MinScrapedAt int64           `json:"min_scraped_at,keepempty"`
}

//ClusterService is a substructure of Cluster containing data for
//a single backend service.
type ClusterService struct {
	Type         string           `json:"type"`
	Resources    ClusterResources `json:"resources,keepempty"`
	MaxScrapedAt int64            `json:"max_scraped_at,keepempty"`
	MinScrapedAt int64            `json:"min_scraped_at,keepempty"`
}

//ClusterResource is a substructure of Cluster containing data for
//a single resource.
type ClusterResource struct {
	Name         string     `json:"name"`
	Unit         limes.Unit `json:"unit,omitempty"`
	Capacity     *uint64    `json:"capacity,keepempty"`
	DomainsQuota uint64     `json:"domains_quota,keepempty"`
	Usage        uint64     `json:"usage,keepempty"`
}

//ClusterServices provides fast lookup of services using a map, but serializes
//to JSON as a list.
type ClusterServices map[string]*ClusterService

//MarshalJSON implements the json.Marshaler interface.
func (s ClusterServices) MarshalJSON() ([]byte, error) {
	//serialize with ordered keys to ensure testcase stability
	types := make([]string, 0, len(s))
	for typeStr := range s {
		types = append(types, typeStr)
	}
	sort.Strings(types)
	list := make([]*ClusterService, len(s))
	for idx, typeStr := range types {
		list[idx] = s[typeStr]
	}
	return json.Marshal(list)
}

//ClusterResources provides fast lookup of resources using a map, but serializes
//to JSON as a list.
type ClusterResources map[string]*ClusterResource

//MarshalJSON implements the json.Marshaler interface.
func (r ClusterResources) MarshalJSON() ([]byte, error) {
	//serialize with ordered keys to ensure testcase stability
	names := make([]string, 0, len(r))
	for name := range r {
		names = append(names, name)
	}
	sort.Strings(names)
	list := make([]*ClusterResource, len(r))
	for idx, name := range names {
		list[idx] = r[name]
	}
	return json.Marshal(list)
}

var clusterReportQuery1 = `
	SELECT d.cluster_id, ps.type, pr.name, SUM(pr.usage), MIN(ps.scraped_at), MAX(ps.scraped_at)
	  FROM domains d
	  JOIN projects p ON p.domain_id = d.id
	  JOIN project_services ps ON ps.project_id = p.id
	  JOIN project_resources pr ON pr.service_id = ps.id
	 WHERE %s GROUP BY d.cluster_id, ps.type, pr.name
`

var clusterReportQuery2 = `
	SELECT d.cluster_id, ds.type, dr.name, SUM(dr.quota)
	  FROM domains d
	  JOIN domain_services ds ON ds.domain_id = d.id
	  JOIN domain_resources dr ON dr.service_id = ds.id
	 WHERE %s GROUP BY d.cluster_id, ds.type, dr.name
`

var clusterReportQuery3 = `
	SELECT cs.cluster_id, cs.type, cr.name, cr.capacity
	  FROM cluster_services cs
	  JOIN cluster_resources cr ON cr.service_id = cs.id
	 WHERE %s
`

//GetClusters returns Cluster reports for al clusters or, if clusterID is
//non-nil, for that cluster only.
//
//In contrast to nearly everything else in Limes, this needs the full
//limes.Configuration (instead of just the current limes.ClusterConfiguration)
//to look at the services enabled in other clusters.
func GetClusters(config limes.Configuration, clusterID *string, dbi db.Interface, filter Filter) ([]*Cluster, error) {
	//TODO: implement (keep in mind the "shared" flag on cluster services)
	return nil, errors.New("stub")
}
