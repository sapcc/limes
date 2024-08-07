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

	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/respondwith"

	"github.com/sapcc/limes/internal/reports"
)

// GetCluster handles GET /v1/clusters/current.
func (p *v1Provider) GetCluster(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/clusters/current")
	token := p.CheckToken(r)
	if !token.Require(w, "cluster:show_basic") {
		return
	}
	showBasic := !token.Check("cluster:show")

	filter := reports.ReadFilter(r, p.Cluster)
	if showBasic {
		filter.IsSubcapacityAllowed = func(serviceType limes.ServiceType, resourceName limesresources.ResourceName) bool {
			token.Context.Request["service"] = string(serviceType)
			token.Context.Request["resource"] = string(resourceName)
			return token.Check("cluster:show_subcapacity")
		}
	}

	cluster, err := reports.GetClusterResources(p.Cluster, p.timeNow(), p.DB, filter)
	if respondwith.ErrorText(w, err) {
		return
	}
	respondwith.JSON(w, 200, map[string]any{"cluster": cluster})
}
