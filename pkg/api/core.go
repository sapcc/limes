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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/mux"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/respondwith"
	"github.com/sapcc/go-bits/sre"
	"github.com/sapcc/limes"
	"github.com/sapcc/limes/pkg/core"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/reports"
)

//VersionData is used by version advertisement handlers.
type VersionData struct {
	Status string            `json:"status"`
	ID     string            `json:"id"`
	Links  []VersionLinkData `json:"links"`
}

//VersionLinkData is used by version advertisement handlers, as part of the
//VersionData struct.
type VersionLinkData struct {
	URL      string `json:"href"`
	Relation string `json:"rel"`
	Type     string `json:"type,omitempty"`
}

type v1Provider struct {
	Cluster        *core.Cluster
	PolicyEnforcer gopherpolicy.Enforcer
	VersionData    VersionData
	//see comment in ListProjects() for details
	listProjectsMutex sync.Mutex
}

//NewV1Router creates a http.Handler that serves the Limes v1 API.
//It also returns the VersionData for this API version which is needed for the
//version advertisement on "GET /".
func NewV1Router(cluster *core.Cluster, policyEnforcer gopherpolicy.Enforcer) (http.Handler, VersionData) {
	r := mux.NewRouter()
	p := &v1Provider{
		Cluster:        cluster,
		PolicyEnforcer: policyEnforcer,
	}
	p.VersionData = VersionData{
		Status: "CURRENT",
		ID:     "v1",
		Links: []VersionLinkData{
			{
				Relation: "self",
				URL:      p.Path(),
			},
			{
				Relation: "describedby",
				URL:      "https://github.com/sapcc/limes/tree/master/docs",
				Type:     "text/html",
			},
		},
	}

	r.Methods("GET").Path("/v1/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respondwith.JSON(w, 200, map[string]interface{}{"version": p.VersionData})
	})

	r.Methods("GET").Path("/v1/clusters/current").HandlerFunc(p.GetCluster)

	r.Methods("GET").Path("/v1/inconsistencies").HandlerFunc(p.ListInconsistencies)

	r.Methods("GET").Path("/v1/domains").HandlerFunc(p.ListDomains)
	r.Methods("GET").Path("/v1/domains/{domain_id}").HandlerFunc(p.GetDomain)
	r.Methods("POST").Path("/v1/domains/discover").HandlerFunc(p.DiscoverDomains)
	r.Methods("POST").Path("/v1/domains/{domain_id}/simulate-put").HandlerFunc(p.SimulatePutDomain)
	r.Methods("PUT").Path("/v1/domains/{domain_id}").HandlerFunc(p.PutDomain)

	r.Methods("GET").Path("/v1/domains/{domain_id}/projects").HandlerFunc(p.ListProjects)
	r.Methods("GET").Path("/v1/domains/{domain_id}/projects/{project_id}").HandlerFunc(p.GetProject)
	r.Methods("POST").Path("/v1/domains/{domain_id}/projects/discover").HandlerFunc(p.DiscoverProjects)
	r.Methods("POST").Path("/v1/domains/{domain_id}/projects/{project_id}/sync").HandlerFunc(p.SyncProject)
	r.Methods("POST").Path("/v1/domains/{domain_id}/projects/{project_id}/simulate-put").HandlerFunc(p.SimulatePutProject)
	r.Methods("PUT").Path("/v1/domains/{domain_id}/projects/{project_id}").HandlerFunc(p.PutProject)

	return sre.Instrument(forbidClusterIDHeader(r)), p.VersionData
}

func forbidClusterIDHeader(inner http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.Header[http.CanonicalHeaderKey("X-Limes-Cluster-Id")]) > 0 {
			http.Error(w, "multi-cluster support is removed: the X-Limes-Cluster-Id header is not allowed anymore", http.StatusBadRequest)
		} else {
			inner.ServeHTTP(w, r)
		}
	})
}

//RequireJSON will parse the request body into the given data structure, or
//write an error response if that fails.
func RequireJSON(w http.ResponseWriter, r *http.Request, data interface{}) bool {
	err := json.NewDecoder(r.Body).Decode(data)
	if err != nil {
		http.Error(w, "request body is not valid JSON: "+err.Error(), 400)
		return false
	}
	return true
}

//Path constructs a full URL for a given URL path below the /v1/ endpoint.
func (p *v1Provider) Path(elements ...string) string {
	parts := []string{
		strings.TrimSuffix(p.Cluster.Config.CatalogURL, "/"),
		"v1",
	}
	parts = append(parts, elements...)
	return strings.Join(parts, "/")
}

//FindDomainFromRequest loads the db.Domain referenced by the :domain_id path
//parameter. Any errors will be written into the response immediately and cause
//a nil return value.
func (p *v1Provider) FindDomainFromRequest(w http.ResponseWriter, r *http.Request) *db.Domain {
	domainUUID := mux.Vars(r)["domain_id"]
	if domainUUID == "" {
		http.Error(w, "domain ID missing", 400)
		return nil
	}

	var domain db.Domain
	err := db.DB.SelectOne(&domain, `SELECT * FROM domains WHERE uuid = $1 AND cluster_id = $2`,
		domainUUID, p.Cluster.ID,
	)
	switch {
	case err == sql.ErrNoRows:
		http.Error(w, "no such domain (if it was just created, try to POST /domains/discover)", 404)
		return nil
	case respondwith.ErrorText(w, err):
		return nil
	default:
		return &domain
	}
}

//FindProjectFromRequest loads the db.Project referenced by the :project_id
//path parameter, and verifies that it is located within the given domain.
func (p *v1Provider) FindProjectFromRequest(w http.ResponseWriter, r *http.Request, domain *db.Domain) *db.Project {
	project, ok := p.FindProjectFromRequestIfExists(w, r, domain)
	if ok && project == nil {
		msg := fmt.Sprintf(
			"no such project (if it was just created, try to POST /domains/%s/projects/discover)",
			mux.Vars(r)["domain_id"],
		)
		http.Error(w, msg, 404)
		return nil
	}
	return project
}

//FindProjectFromRequestIfExists works like FindProjectFromRequest, but returns
//a nil project instead of producing an error if the project does not exist in
//the local DB yet.
func (p *v1Provider) FindProjectFromRequestIfExists(w http.ResponseWriter, r *http.Request, domain *db.Domain) (project *db.Project, ok bool) {
	projectUUID := mux.Vars(r)["project_id"]
	if projectUUID == "" {
		http.Error(w, "project ID missing", 400)
		return nil, false
	}

	project = &db.Project{}
	err := db.DB.SelectOne(project, `SELECT * FROM projects WHERE uuid = $1`, projectUUID)
	switch {
	case err == sql.ErrNoRows:
		return nil, true
	case err == nil && domain.ID != project.DomainID:
		http.Error(w, "no such project", 404)
		return nil, false
	case respondwith.ErrorText(w, err):
		return nil, false
	default:
		return project, true
	}
}

//GetDomainReport is a convenience wrapper around reports.GetDomains() for getting a single domain report.
func GetDomainReport(cluster *core.Cluster, dbDomain db.Domain, dbi db.Interface, filter reports.Filter) (*limes.DomainReport, error) {
	domainReports, err := reports.GetDomains(cluster, &dbDomain.ID, dbi, filter)
	if err != nil {
		return nil, err
	}
	if len(domainReports) == 0 {
		return nil, errors.New("no resource data found for domain")
	}
	return domainReports[0], nil
}

//GetProjectReport is a convenience wrapper around reports.GetProjects() for getting a single project report.
func GetProjectReport(cluster *core.Cluster, dbDomain db.Domain, dbProject db.Project, dbi db.Interface, filter reports.Filter) (*limes.ProjectReport, error) {
	projectReports, err := reports.GetProjects(cluster, dbDomain, &dbProject.ID, dbi, filter)
	if err != nil {
		return nil, err
	}
	if len(projectReports) == 0 {
		return nil, errors.New("no resource data found for project")
	}
	return projectReports[0], nil
}
