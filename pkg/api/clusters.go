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
	"github.com/sapcc/go-bits/respondwith"
	"github.com/sapcc/go-bits/sre"
	"github.com/sapcc/limes"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/reports"
)

//ListClusters handles GET /v1/clusters.
func (p *v1Provider) ListClusters(w http.ResponseWriter, r *http.Request) {
	sre.IdentifyEndpoint(r, "/v1/clusters")
	token := p.CheckToken(r)
	if !token.Require(w, "cluster:list") {
		return
	}
	currentCluster := p.FindClusterFromRequest(w, r, token)
	if currentCluster == nil {
		return
	}

	var result struct {
		CurrentCluster string                 `json:"current_cluster"`
		Clusters       []*limes.ClusterReport `json:"clusters"`
	}
	result.CurrentCluster = currentCluster.ID

	var err error
	result.Clusters, err = reports.GetClusters(p.Config, nil, db.DB, reports.ReadFilter(r))
	if respondwith.ErrorText(w, err) {
		return
	}

	respondwith.JSON(w, 200, result)
}

//GetCluster handles GET /v1/clusters/:cluster_id.
func (p *v1Provider) GetCluster(w http.ResponseWriter, r *http.Request) {
	sre.IdentifyEndpoint(r, "/v1/clusters/:id")
	token := p.CheckToken(r)
	if !token.Require(w, "cluster:show_basic") {
		return
	}
	showBasic := !token.Check("cluster:show")

	clusterID := mux.Vars(r)["cluster_id"]
	currentClusterID := p.Cluster.ID
	if clusterID == "current" {
		clusterID = currentClusterID
	}
	if showBasic && (clusterID != currentClusterID) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	filter := reports.ReadFilter(r)
	if showBasic && (filter.WithSubresources || filter.WithSubcapacities || filter.LocalQuotaUsageOnly) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	clusters, err := reports.GetClusters(p.Config, &clusterID, db.DB, filter)
	if respondwith.ErrorText(w, err) {
		return
	}
	if len(clusters) == 0 {
		http.Error(w, "no such cluster", 404)
		return
	}

	respondwith.JSON(w, 200, map[string]interface{}{"cluster": clusters[0]})
}
