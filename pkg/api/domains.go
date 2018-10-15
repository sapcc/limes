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
	"time"

	gorp "gopkg.in/gorp.v2"

	"github.com/sapcc/go-bits/respondwith"
	"github.com/sapcc/limes/pkg/collector"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/reports"
	"github.com/sapcc/limes/pkg/util"
)

//ListDomains handles GET /v1/domains.
func (p *v1Provider) ListDomains(w http.ResponseWriter, r *http.Request) {
	token := p.CheckToken(r)
	if !token.Require(w, "domain:list") {
		return
	}
	cluster := p.FindClusterFromRequest(w, r, token)
	if cluster == nil {
		return
	}

	domains, err := reports.GetDomains(cluster, nil, db.DB, reports.ReadFilter(r))
	if respondwith.ErrorText(w, err) {
		return
	}

	respondwith.JSON(w, 200, map[string]interface{}{"domains": domains})
}

//GetDomain handles GET /v1/domains/:domain_id.
func (p *v1Provider) GetDomain(w http.ResponseWriter, r *http.Request) {
	token := p.CheckToken(r)
	if !token.Require(w, "domain:show") {
		return
	}
	cluster := p.FindClusterFromRequest(w, r, token)
	if cluster == nil {
		return
	}
	dbDomain := p.FindDomainFromRequest(w, r, cluster)
	if dbDomain == nil {
		return
	}

	domain, err := GetDomainReport(cluster, *dbDomain, db.DB, reports.ReadFilter(r))
	if respondwith.ErrorText(w, err) {
		return
	}
	respondwith.JSON(w, 200, map[string]interface{}{"domain": domain})
}

//DiscoverDomains handles POST /v1/domains/discover.
func (p *v1Provider) DiscoverDomains(w http.ResponseWriter, r *http.Request) {
	token := p.CheckToken(r)
	if !token.Require(w, "domain:discover") {
		return
	}
	cluster := p.FindClusterFromRequest(w, r, token)
	if cluster == nil {
		return
	}

	newDomainUUIDs, err := collector.ScanDomains(cluster, collector.ScanDomainsOpts{})
	if respondwith.ErrorText(w, err) {
		return
	}

	if len(newDomainUUIDs) == 0 {
		w.WriteHeader(204)
		return
	}
	respondwith.JSON(w, 202, map[string]interface{}{"new_domains": util.IDsToJSON(newDomainUUIDs)})
}

//PutDomain handles PUT /v1/domains/:domain_id.
func (p *v1Provider) PutDomain(w http.ResponseWriter, r *http.Request) {
	requestTime := time.Now()
	token := p.CheckToken(r)
	canRaise := token.Check("domain:raise")
	canLower := token.Check("domain:lower")
	if !canRaise && !canLower {
		token.Require(w, "domain:raise") //produce standard Unauthorized response
		return
	}

	updater := QuotaUpdater{CanRaise: canRaise, CanLower: canLower}
	updater.Cluster = p.FindClusterFromRequest(w, r, token)
	if updater.Cluster == nil {
		return
	}
	updater.Domain = p.FindDomainFromRequest(w, r, updater.Cluster)
	if updater.Domain == nil {
		return
	}

	//parse request body
	var parseTarget struct {
		Domain struct {
			Services ServiceQuotas `json:"services"`
		} `json:"domain"`
	}
	parseTarget.Domain.Services = make(ServiceQuotas)
	if !RequireJSON(w, r, &parseTarget) {
		return
	}

	//start a transaction for the quota updates
	tx, err := db.DB.Begin()
	if respondwith.ErrorText(w, err) {
		return
	}
	defer db.RollbackUnlessCommitted(tx)

	//validate inputs (within the DB transaction, to ensure that we do not apply
	//inconsistent values later)
	err = updater.ValidateInput(parseTarget.Domain.Services, tx)
	if respondwith.ErrorText(w, err) {
		return
	}
	if !updater.IsValid() {
		updater.CommitAuditTrail(token, r, requestTime)
		http.Error(w, updater.ErrorMessage(), http.StatusUnprocessableEntity)
		return
	}

	//check all services for resources to update
	var services []db.DomainService
	_, err = tx.Select(&services,
		`SELECT * FROM domain_services WHERE domain_id = $1 ORDER BY type`, updater.Domain.ID)
	if respondwith.ErrorText(w, err) {
		return
	}
	var resourcesToUpdate []interface{}

	for _, srv := range services {
		serviceRequests, exists := updater.Requests[srv.Type]
		if !exists {
			continue
		}
		isExistingResource := make(map[string]bool)

		//check all existing resources
		var resources []db.DomainResource
		_, err = tx.Select(&resources,
			`SELECT * FROM domain_resources WHERE service_id = $1 ORDER BY name`, srv.ID)
		if respondwith.ErrorText(w, err) {
			return
		}
		for _, res := range resources {
			isExistingResource[res.Name] = true
			req, exists := serviceRequests[res.Name]
			if !exists {
				continue
			}
			if res.Quota == req.NewValue {
				continue //nothing to do
			}

			//take a copy of the loop variable (it will be updated by the loop, so if
			//we didn't take a copy manually, the resourcesToUpdate list
			//would contain only identical pointers)
			res := res

			res.Quota = req.NewValue
			resourcesToUpdate = append(resourcesToUpdate, &res)
		}

		//check resources that need to be created
		for resourceName, req := range serviceRequests {
			if isExistingResource[resourceName] {
				continue
			}

			err = tx.Insert(&db.DomainResource{
				ServiceID: srv.ID,
				Name:      resourceName,
				Quota:     req.NewValue,
			})
			if respondwith.ErrorText(w, err) {
				return
			}
		}
	}

	//update the DB with the new quotas
	onlyQuota := func(c *gorp.ColumnMap) bool {
		return c.ColumnName == "quota"
	}
	_, err = tx.UpdateColumns(onlyQuota, resourcesToUpdate...)
	if respondwith.ErrorText(w, err) {
		return
	}
	err = tx.Commit()
	if respondwith.ErrorText(w, err) {
		return
	}
	updater.CommitAuditTrail(token, r, requestTime)

	//report success
	w.WriteHeader(202)
}
