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

//ListInconsistencies handles GET /v1/admin/scrape-errors.
func (p *v1Provider) ListScrapeErrors(w http.ResponseWriter, r *http.Request) {
	sre.IdentifyEndpoint(r, "/v1/admin/scrape-errors")
	token := p.CheckToken(r)
	if !token.Require(w, "cluster:show") {
		return
	}

	scrapeErrors, err := reports.GetScrapeErrors(p.Cluster, db.DB, reports.ReadFilter(r))
	if respondwith.ErrorText(w, err) {
		return
	}

	respondwith.JSON(w, http.StatusOK, map[string]interface{}{"scrape_errors": scrapeErrors})
}
