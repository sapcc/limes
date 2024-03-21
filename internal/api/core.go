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
	"html"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-gorp/gorp/v3"
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gorilla/mux"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/osext"
	"github.com/sapcc/go-bits/respondwith"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/reports"
)

// VersionData is used by version advertisement handlers.
type VersionData struct {
	Status string            `json:"status"`
	ID     string            `json:"id"`
	Links  []VersionLinkData `json:"links"`
}

// VersionLinkData is used by version advertisement handlers, as part of the
// VersionData struct.
type VersionLinkData struct {
	URL      string `json:"href"`
	Relation string `json:"rel"`
	Type     string `json:"type,omitempty"`
}

type v1Provider struct {
	Cluster        *core.Cluster
	DB             *gorp.DbMap
	VersionData    VersionData
	tokenValidator gopherpolicy.Validator
	// see comment in ListProjects() for details
	listProjectsMutex sync.Mutex
	// slots for test doubles
	timeNow func() time.Time
	// identifies commitments that will be transferred to other projects.
	generateTransferToken func() string
}

// NewV1API creates an httpapi.API that serves the Limes v1 API.
// It also returns the VersionData for this API version which is needed for the
// version advertisement on "GET /".
func NewV1API(cluster *core.Cluster, dbm *gorp.DbMap, tokenValidator gopherpolicy.Validator, timeNow func() time.Time, generateTransferToken func() string) httpapi.API {
	p := &v1Provider{Cluster: cluster, DB: dbm, tokenValidator: tokenValidator, timeNow: timeNow}
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
				URL:      "https://github.com/sapcc/limes/blob/master/docs/users/api-v1-specification.md",
				Type:     "text/html",
			},
		},
	}
	p.generateTransferToken = generateTransferToken

	return p
}

// NewTokenValidator constructs a gopherpolicy.TokenValidator instance.
func NewTokenValidator(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (gopherpolicy.Validator, error) {
	identityV3, err := openstack.NewIdentityV3(provider, eo)
	if err != nil {
		return nil, fmt.Errorf("cannot initialize Keystone v3 client: %w", err)
	}
	tv := gopherpolicy.TokenValidator{
		IdentityV3: identityV3,
		Cacher:     gopherpolicy.InMemoryCacher(),
	}
	err = tv.LoadPolicyFile(osext.GetenvOrDefault("LIMES_API_POLICY_PATH", "/etc/limes/policy.yaml"))
	return &tv, err
}

func (p *v1Provider) OverrideGenerateTransferToken(generateTransferToken func() string) *v1Provider {
	p.generateTransferToken = generateTransferToken
	return p
}

// AddTo implements the httpapi.API interface.
func (p *v1Provider) AddTo(r *mux.Router) {
	r.Methods("HEAD", "GET").Path("/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpapi.IdentifyEndpoint(r, "/")
		httpapi.SkipRequestLog(r)
		respondwith.JSON(w, 300, map[string]any{"versions": []VersionData{p.VersionData}})
	})

	r.Methods("GET").Path("/v1/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpapi.IdentifyEndpoint(r, "/v1/")
		httpapi.SkipRequestLog(r)
		respondwith.JSON(w, 200, map[string]any{"version": p.VersionData})
	})

	r.Methods("GET").Path("/v1/clusters/current").HandlerFunc(p.GetCluster)
	r.Methods("GET").Path("/rates/v1/clusters/current").HandlerFunc(p.GetClusterRates)

	r.Methods("GET").Path("/v1/inconsistencies").HandlerFunc(p.ListInconsistencies)
	r.Methods("GET").Path("/v1/admin/scrape-errors").HandlerFunc(p.ListScrapeErrors)
	r.Methods("GET").Path("/rates/v1/admin/scrape-errors").HandlerFunc(p.ListRateScrapeErrors)

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
	r.Methods("GET").Path("/rates/v1/domains/{domain_id}/projects").HandlerFunc(p.ListProjectRates)
	r.Methods("GET").Path("/rates/v1/domains/{domain_id}/projects/{project_id}").HandlerFunc(p.GetProjectRates)
	r.Methods("POST").Path("/rates/v1/domains/{domain_id}/projects/{project_id}/sync").HandlerFunc(p.SyncProjectRates)
	r.Methods("POST").Path("/rates/v1/domains/{domain_id}/projects/{project_id}/simulate-put").HandlerFunc(p.SimulatePutProjectRates)
	r.Methods("PUT").Path("/rates/v1/domains/{domain_id}/projects/{project_id}").HandlerFunc(p.PutProjectRates)

	r.Methods("GET").Path("/v1/domains/{domain_id}/projects/{project_id}/commitments").HandlerFunc(p.GetProjectCommitments)
	r.Methods("POST").Path("/v1/domains/{domain_id}/projects/{project_id}/commitments/new").HandlerFunc(p.CreateProjectCommitment)
	r.Methods("POST").Path("/v1/domains/{domain_id}/projects/{project_id}/commitments/can-confirm").HandlerFunc(p.CanConfirmNewProjectCommitment)
	r.Methods("DELETE").Path("/v1/domains/{domain_id}/projects/{project_id}/commitments/{id}").HandlerFunc(p.DeleteProjectCommitment)
	r.Methods("POST").Path("/v1/domains/{domain_id}/projects/{project_id}/commitments/{id}/start-transfer").HandlerFunc(p.StartCommitmentTransfer)
	r.Methods("POST").Path("/v1/domains/{domain_id}/projects/{project_id}/transfer-commitment/{id}").HandlerFunc(p.TransferCommitment)
}

// RequireJSON will parse the request body into the given data structure, or
// write an error response if that fails.
func RequireJSON(w http.ResponseWriter, r *http.Request, data any) bool {
	err := json.NewDecoder(r.Body).Decode(data)
	if err != nil {
		http.Error(w, "request body is not valid JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

// Path constructs a full URL for a given URL path below the /v1/ endpoint.
func (p *v1Provider) Path(elements ...string) string {
	parts := []string{
		strings.TrimSuffix(p.Cluster.Config.CatalogURL, "/"),
		"v1",
	}
	parts = append(parts, elements...)
	return strings.Join(parts, "/")
}

// CheckToken checks the validity of the request's X-Auth-Token in Keystone, and
// returns a Token instance for checking authorization. Any errors that occur
// during this function are deferred until Require() is called.
func (p *v1Provider) CheckToken(r *http.Request) *gopherpolicy.Token {
	t := p.tokenValidator.CheckToken(r)
	t.Context.Request = mux.Vars(r)
	return t
}

// FindDomainFromRequest loads the db.Domain referenced by the :domain_id path
// parameter. Any errors will be written into the response immediately and cause
// a nil return value.
func (p *v1Provider) FindDomainFromRequest(w http.ResponseWriter, r *http.Request) *db.Domain {
	domainUUID := mux.Vars(r)["domain_id"]
	if domainUUID == "" {
		http.Error(w, "domain ID missing", http.StatusBadRequest)
		return nil
	}

	var domain db.Domain
	err := p.DB.SelectOne(&domain, `SELECT * FROM domains WHERE uuid = $1`, domainUUID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		http.Error(w, "no such domain (if it was just created, try to POST /domains/discover)", http.StatusNotFound)
		return nil
	case respondwith.ErrorText(w, err):
		return nil
	default:
		return &domain
	}
}

// FindProjectFromRequest loads the db.Project referenced by the :project_id
// path parameter, and verifies that it is located within the given domain.
func (p *v1Provider) FindProjectFromRequest(w http.ResponseWriter, r *http.Request, domain *db.Domain) *db.Project {
	project, ok := p.FindProjectFromRequestIfExists(w, r, domain)
	if ok && project == nil {
		msg := fmt.Sprintf(
			"no such project (if it was just created, try to POST /domains/%s/projects/discover)",
			html.EscapeString(mux.Vars(r)["domain_id"]),
		)
		http.Error(w, msg, http.StatusNotFound)
		return nil
	}
	return project
}

// FindProjectFromRequestIfExists works like FindProjectFromRequest, but returns
// a nil project instead of producing an error if the project does not exist in
// the local DB yet.
func (p *v1Provider) FindProjectFromRequestIfExists(w http.ResponseWriter, r *http.Request, domain *db.Domain) (project *db.Project, ok bool) {
	projectUUID := mux.Vars(r)["project_id"]
	if projectUUID == "" {
		http.Error(w, "project ID missing", http.StatusBadRequest)
		return nil, false
	}

	project = &db.Project{}
	err := p.DB.SelectOne(project, `SELECT * FROM projects WHERE uuid = $1`, projectUUID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, true
	case err == nil && domain.ID != project.DomainID:
		http.Error(w, "no such project", http.StatusNotFound)
		return nil, false
	case respondwith.ErrorText(w, err):
		return nil, false
	default:
		return project, true
	}
}

// GetDomainReport is a convenience wrapper around reports.GetDomains() for getting a single domain report.
func GetDomainReport(cluster *core.Cluster, dbDomain db.Domain, now time.Time, dbi db.Interface, filter reports.Filter) (*limesresources.DomainReport, error) {
	domainReports, err := reports.GetDomains(cluster, &dbDomain.ID, now, dbi, filter)
	if err != nil {
		return nil, err
	}
	if len(domainReports) == 0 {
		return nil, errors.New("no resource data found for domain")
	}
	return domainReports[0], nil
}

// GetProjectResourceReport is a convenience wrapper around reports.GetProjectResources() for getting a single project resource report.
func GetProjectResourceReport(cluster *core.Cluster, dbDomain db.Domain, dbProject db.Project, now time.Time, dbi db.Interface, filter reports.Filter) (*limesresources.ProjectReport, error) {
	var result *limesresources.ProjectReport
	err := reports.GetProjectResources(cluster, dbDomain, &dbProject, now, dbi, filter, func(r *limesresources.ProjectReport) error {
		result = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("no resource data found for project")
	}
	return result, nil
}

// GetProjectRateReport is a convenience wrapper around reports.GetProjectRates() for getting a single project rate report.
func GetProjectRateReport(cluster *core.Cluster, dbDomain db.Domain, dbProject db.Project, dbi db.Interface, filter reports.Filter) (*limesrates.ProjectReport, error) {
	var result *limesrates.ProjectReport
	err := reports.GetProjectRates(cluster, dbDomain, &dbProject, dbi, filter, func(r *limesrates.ProjectReport) error {
		result = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("no resource data found for project")
	}
	return result, nil
}
