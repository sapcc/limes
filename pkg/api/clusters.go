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
	"database/sql"
	"fmt"
	"net/http"
	"strings"

	gorp "gopkg.in/gorp.v2"

	"github.com/gorilla/mux"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/reports"
)

//ListClusters handles GET /v1/clusters.
func (p *v1Provider) ListClusters(w http.ResponseWriter, r *http.Request) {
	if !p.CheckToken(r).Require(w, "cluster:list") {
		return
	}

	var result struct {
		CurrentCluster string             `json:"current_cluster"`
		Clusters       []*reports.Cluster `json:"clusters"`
	}
	result.CurrentCluster = p.Cluster.ID

	var err error
	_, localQuotaUsageOnly := r.URL.Query()["local"]
	_, withSubcapacities := r.URL.Query()["detail"]
	result.Clusters, err = reports.GetClusters(p.Config, nil, localQuotaUsageOnly, withSubcapacities, db.DB, reports.ReadFilter(r))
	if ReturnError(w, err) {
		return
	}

	ReturnJSON(w, 200, result)
}

//GetCluster handles GET /v1/clusters/:cluster_id.
func (p *v1Provider) GetCluster(w http.ResponseWriter, r *http.Request) {
	if !p.CheckToken(r).Require(w, "cluster:show") {
		return
	}

	clusterID := mux.Vars(r)["cluster_id"]
	if clusterID == "current" {
		clusterID = p.Cluster.ID
	}
	_, localQuotaUsageOnly := r.URL.Query()["local"]
	_, withSubcapacities := r.URL.Query()["detail"]
	clusters, err := reports.GetClusters(p.Config, &clusterID, localQuotaUsageOnly, withSubcapacities, db.DB, reports.ReadFilter(r))
	if ReturnError(w, err) {
		return
	}
	if len(clusters) == 0 {
		http.Error(w, "no such cluster", 404)
		return
	}

	ReturnJSON(w, 200, map[string]interface{}{"cluster": clusters[0]})
}

//PutCluster handles PUT /v1/clusters/:cluster_id.
func (p *v1Provider) PutCluster(w http.ResponseWriter, r *http.Request) {
	if !p.CheckToken(r).Require(w, "cluster:edit") {
		return
	}

	//check whether cluster exists
	clusterID := mux.Vars(r)["cluster_id"]
	if clusterID == "current" {
		clusterID = p.Cluster.ID
	}
	cluster, ok := p.Config.Clusters[clusterID]
	if !ok {
		http.Error(w, "no such cluster", 404)
		return
	}

	//parse request body
	var parseTarget struct {
		Cluster struct {
			Services []ServiceCapacities `json:"services"`
		} `json:"cluster"`
	}
	if !RequireJSON(w, r, &parseTarget) {
		return
	}

	//start a transaction for the capacity updates
	tx, err := db.DB.Begin()
	if ReturnError(w, err) {
		return
	}
	defer db.RollbackUnlessCommitted(tx)

	var errors []string

	for _, srv := range parseTarget.Cluster.Services {
		//check that this service is configured for this cluster
		if !cluster.HasService(srv.Type) {
			for _, res := range srv.Resources {
				errors = append(errors,
					fmt.Sprintf("cannot set %s/%s capacity: no such service", srv.Type, res.Name),
				)
			}
			continue
		}

		service, err := findClusterService(tx, srv, clusterID, cluster.IsServiceShared[srv.Type])
		if ReturnError(w, err) {
			return
		}
		if service == nil {
			//this should only occur if a service was added, and users try to
			//maintain capacity for the new service before CheckConsistency() has run
			//(which should happen immediately when `limes collect` starts)
			for _, res := range srv.Resources {
				errors = append(errors,
					fmt.Sprintf("cannot set %s/%s capacity: no such service", srv.Type, res.Name),
				)
			}
			continue
		}

		for _, res := range srv.Resources {
			msg, err := writeClusterResource(tx, cluster, srv, service, res)
			if ReturnError(w, err) {
				return
			}
			if msg != "" {
				errors = append(errors,
					fmt.Sprintf("cannot set %s/%s capacity: %s", srv.Type, res.Name, msg),
				)
			}
		}

		//TODO: when deleting all cluster_resources associated with a single
		//cluster_services record, cleanup the cluster_services record, too
	}

	//if not legal, report errors to the user
	if len(errors) > 0 {
		http.Error(w, strings.Join(errors, "\n"), 422)
		return
	}
	err = tx.Commit()
	if ReturnError(w, err) {
		return
	}

	//otherwise, report success
	clusters, err := reports.GetClusters(p.Config, &clusterID, false, false, db.DB, reports.ReadFilter(r))
	if ReturnError(w, err) {
		return
	}
	if len(clusters) == 0 {
		http.Error(w, "no resource data found for cluster", 500)
		return
	}

	ReturnJSON(w, 200, map[string]interface{}{"cluster": clusters[0]})
}

func findClusterService(tx *gorp.Transaction, srv ServiceCapacities, clusterID string, shared bool) (*db.ClusterService, error) {
	if shared {
		clusterID = "shared"
	}
	var service *db.ClusterService
	err := tx.SelectOne(&service,
		`SELECT * FROM cluster_services WHERE cluster_id = $1 AND type = $2`,
		clusterID, srv.Type,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	return service, nil
}

func writeClusterResource(tx *gorp.Transaction, cluster *limes.Cluster, srv ServiceCapacities, service *db.ClusterService, res ResourceCapacity) (validationError string, internalError error) {
	if !cluster.HasResource(srv.Type, res.Name) {
		return "no such resource", nil
	}

	//load existing resource record, if any
	var resource *db.ClusterResource
	err := tx.SelectOne(&resource,
		`SELECT * FROM cluster_resources WHERE service_id = $1 AND name = $2`,
		service.ID, res.Name,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			resource = nil
		} else {
			return "", err
		}
	}

	//easiest case: if deletion is requested and the record is deleted, we're done
	if resource == nil && res.Capacity < 0 {
		return "", nil
	}

	//validation
	if resource != nil && resource.Comment == "" {
		return "capacity for this resource is maintained automatically", nil
	}
	if res.Capacity >= 0 && res.Comment == "" {
		return "comment is missing", nil
	}

	//convert to target unit if required
	var newCapacity uint64
	if res.Capacity >= 0 {
		inputUnit := limes.UnitUnspecified
		if res.Unit != nil {
			inputUnit = *res.Unit
		}
		//int64->uint64 is safe here because `res.Capacity >= 0` has already been established
		inputValue := limes.ValueWithUnit{Value: uint64(res.Capacity), Unit: inputUnit}
		newCapacity, err = inputValue.ConvertFor(cluster, srv.Type, res.Name)
		if err != nil {
			return err.Error(), nil
		}
	}

	switch {
	case resource == nil:
		//need to insert
		resource = &db.ClusterResource{
			ServiceID: service.ID,
			Name:      res.Name,
			Capacity:  newCapacity,
			Comment:   res.Comment,
		}
		return "", tx.Insert(resource)
	case res.Capacity < 0:
		//need to delete
		_, err := tx.Delete(resource)
		return "", err
	default:
		//need to update
		resource.Capacity = newCapacity
		resource.Comment = res.Comment
		_, err := tx.Update(resource)
		return "", err
	}
}
