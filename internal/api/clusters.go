// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"

	"github.com/go-gorp/gorp/v3"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/respondwith"

	"github.com/sapcc/limes/internal/db"
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

	serviceInfos, err := p.Cluster.AllServiceInfos()
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	filter := reports.ReadFilter(r, p.Cluster, serviceInfos)
	if showBasic {
		filter.IsSubcapacityAllowed = func(serviceType db.ServiceType, resourceName liquid.ResourceName) bool {
			token.Context.Request["service"] = string(serviceType)
			token.Context.Request["resource"] = string(resourceName)
			return token.Check("cluster:show_subcapacity")
		}
	}

	var cluster *limesresources.ClusterReport
	err = db.RunOLAPQueries(p.DB, func(tx *gorp.Transaction) (err error) {
		cluster, err = reports.GetClusterResources(p.Cluster, p.timeNow(), p.DB, filter, serviceInfos)
		return err
	})
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	respondwith.JSON(w, 200, map[string]any{"cluster": cluster})
}
