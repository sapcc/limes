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

	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/respondwith"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/pkg/collector"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/reports"
	"github.com/sapcc/limes/pkg/util"
)

// ListDomains handles GET /v1/domains.
func (p *v1Provider) ListDomains(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains")
	token := p.CheckToken(r)
	if !token.Require(w, "domain:list") {
		return
	}

	domains, err := reports.GetDomains(p.Cluster, nil, db.DB, reports.ReadFilter(r))
	if respondwith.ErrorText(w, err) {
		return
	}

	respondwith.JSON(w, 200, map[string]interface{}{"domains": domains})
}

// GetDomain handles GET /v1/domains/:domain_id.
func (p *v1Provider) GetDomain(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains/:id")
	token := p.CheckToken(r)
	if !token.Require(w, "domain:show") {
		return
	}
	dbDomain := p.FindDomainFromRequest(w, r)
	if dbDomain == nil {
		return
	}

	domain, err := GetDomainReport(p.Cluster, *dbDomain, db.DB, reports.ReadFilter(r))
	if respondwith.ErrorText(w, err) {
		return
	}
	respondwith.JSON(w, 200, map[string]interface{}{"domain": domain})
}

// DiscoverDomains handles POST /v1/domains/discover.
func (p *v1Provider) DiscoverDomains(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains/discover")
	token := p.CheckToken(r)
	if !token.Require(w, "domain:discover") {
		return
	}

	newDomainUUIDs, err := collector.ScanDomains(p.Cluster, collector.ScanDomainsOpts{})
	if respondwith.ErrorText(w, err) {
		return
	}

	if len(newDomainUUIDs) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	respondwith.JSON(w, 202, map[string]interface{}{"new_domains": util.IDsToJSON(newDomainUUIDs)})
}

// PutDomain handles PUT /v1/domains/:domain_id.
func (p *v1Provider) PutDomain(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains/:id")
	p.putOrSimulatePutDomain(w, r, false)
}

// SimulatePutDomain handles POST /v1/domains/:domain_id/simulate-put.
func (p *v1Provider) SimulatePutDomain(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains/:id/simulate-put")
	p.putOrSimulatePutDomain(w, r, true)
}

func (p *v1Provider) putOrSimulatePutDomain(w http.ResponseWriter, r *http.Request, simulate bool) {
	requestTime := time.Now()
	token := p.CheckToken(r)
	if !token.Require(w, "domain:show") {
		return
	}
	checkToken := func(policy string) func(string) bool {
		return func(serviceType string) bool {
			token.Context.Request["service_type"] = serviceType
			return token.Check(policy)
		}
	}

	updater := QuotaUpdater{
		Cluster:    p.Cluster,
		CanRaise:   checkToken("domain:raise"),
		CanRaiseLP: checkToken("domain:raise_lowpriv"),
		CanLower:   checkToken("domain:lower"),
	}
	updater.Domain = p.FindDomainFromRequest(w, r)
	if updater.Domain == nil {
		return
	}

	//parse request body
	var parseTarget struct {
		Domain struct {
			Services limesresources.QuotaRequest `json:"services"`
		} `json:"domain"`
	}
	parseTarget.Domain.Services = make(limesresources.QuotaRequest)
	if !RequireJSON(w, r, &parseTarget) {
		return
	}

	//start a transaction for the quota updates
	var tx *gorp.Transaction
	var dbi db.Interface
	if simulate {
		dbi = db.DB
	} else {
		var err error
		tx, err = db.DB.Begin()
		if respondwith.ErrorText(w, err) {
			return
		}
		defer sqlext.RollbackUnlessCommitted(tx)
		dbi = tx
	}

	//validate inputs (within the DB transaction, to ensure that we do not apply
	//inconsistent values later)
	err := updater.ValidateInput(parseTarget.Domain.Services, dbi)
	if respondwith.ErrorText(w, err) {
		return
	}

	//stop now if we're only simulating
	if simulate {
		updater.WriteSimulationReport(w)
		return
	}

	if !updater.IsValid() {
		updater.CommitAuditTrail(token, r, requestTime)
		updater.WritePutErrorResponse(w)
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
	w.WriteHeader(http.StatusAccepted)
}
