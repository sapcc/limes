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
	"net/http"

	"github.com/gorilla/mux"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/reports"
)

//ListClusters handles GET /v1/clusters.
func (p *v1Provider) ListClusters(w http.ResponseWriter, r *http.Request) {
	if !p.CheckToken(r).Require(w, "cluster:list") {
		return
	}

	clusters, err := reports.GetClusters(p.Config, nil, db.DB, reports.ReadFilter(r))
	if ReturnError(w, err) {
		return
	}

	ReturnJSON(w, 200, map[string]interface{}{"clusters": clusters})
}

//GetCluster handles GET /v1/clusters/:cluster_id.
func (p *v1Provider) GetCluster(w http.ResponseWriter, r *http.Request) {
	if !p.CheckToken(r).Require(w, "cluster:show") {
		return
	}

	clusterID := mux.Vars(r)["cluster_id"]
	if clusterID == "current" {
		clusterID = p.Driver.Cluster().ID
	}
	clusters, err := reports.GetClusters(p.Config, &clusterID, db.DB, reports.ReadFilter(r))
	if ReturnError(w, err) {
		return
	}
	if len(clusters) == 0 {
		http.Error(w, "no such cluster", 404)
		return
	}

	ReturnJSON(w, 200, map[string]interface{}{"cluster": clusters[0]})
}
