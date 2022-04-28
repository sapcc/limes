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

package api

import (
	"net/http"

	"github.com/sapcc/go-bits/respondwith"
	"github.com/sapcc/go-bits/sre"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/reports"
)

//GetClusterRates handles GET /v1/clusters/:cluster_id.
func (p *v1Provider) GetClusterRates(w http.ResponseWriter, r *http.Request) {
	sre.IdentifyEndpoint(r, "/rates/v1/clusters/current")
	token := p.CheckToken(r)
	if !token.Require(w, "cluster:show") {
		return
	}

	if _, ok := r.URL.Query()["rates"]; ok {
		http.Error(w, "the `rates` query parameter is not allowed here", http.StatusBadRequest)
		return
	}

	filter := reports.ReadFilter(r)
	filter.WithRates = true
	filter.OnlyRates = true

	cluster, err := reports.GetCluster(p.Cluster, db.DB, filter)
	if respondwith.ErrorText(w, err) {
		return
	}
	respondwith.JSON(w, 200, map[string]interface{}{"cluster": cluster})
}
