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
	"time"

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

//PutCluster handles PUT /v1/clusters/:cluster_id.
func (p *v1Provider) PutCluster(w http.ResponseWriter, r *http.Request) {
	if !p.CheckToken(r).Require(w, "cluster:edit") {
		return
	}

	//check whether cluster exists
	clusterID := mux.Vars(r)["cluster_id"]
	if clusterID == "current" {
		clusterID = p.Driver.Cluster().ID
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

		service, err := findOrCreateClusterService(tx, srv, clusterID, cluster.IsServiceShared[srv.Type])
		if ReturnError(w, err) {
			return
		}
		if service == nil {
			//this occurs if the cluster_services record does not exist, and we also
			//don't need to create it because all srv.Resources are set to be deleted,
			//therefore we're done with this service
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
	clusters, err := reports.GetClusters(p.Config, &clusterID, db.DB, reports.ReadFilter(r))
	if ReturnError(w, err) {
		return
	}
	if len(clusters) == 0 {
		http.Error(w, "no resource data found for cluster", 500)
		return
	}

	ReturnJSON(w, 200, map[string]interface{}{"cluster": clusters[0]})
}

func findOrCreateClusterService(tx *gorp.Transaction, srv ServiceCapacities, clusterID string, shared bool) (*db.ClusterService, error) {
	needToCreateService := false
	for _, res := range srv.Resources {
		if res.Capacity >= 0 {
			needToCreateService = true
			break
		}
	}
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
			if !needToCreateService {
				return nil, nil
			}
			now := time.Now()
			service = &db.ClusterService{
				ClusterID: clusterID,
				Type:      srv.Type,
				ScrapedAt: &now,
			}
			err := tx.Insert(service)
			if err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
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

	switch {
	case resource == nil:
		//need to insert
		resource = &db.ClusterResource{
			ServiceID: service.ID,
			Name:      res.Name,
			//int64->uint64 cast is safe here because `res.Capacity >= 0` is already known
			Capacity: uint64(res.Capacity),
			Comment:  res.Comment,
		}
		return "", tx.Insert(resource)
	case res.Capacity < 0:
		//need to delete
		_, err := tx.Delete(resource)
		return "", err
	default:
		//need to update
		resource.Capacity = uint64(res.Capacity) //cast is safe here just like above
		resource.Comment = res.Comment
		_, err := tx.Update(resource)
		return "", err
	}
}
