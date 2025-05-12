/******************************************************************************
*
*  Copyright 2017-2023 SAP SE
*
*  Licensed under the Apache License, Version 2.0 (the "License");
*  you may not use this file except in compliance with the License.
*  You may obtain a copy of the License at
*
*      http://www.apache.org/licenses/LICENSE-2.0
*
*  Unless required by applicable law or agreed to in writing, software
*  distributed under the License is distributed on an "AS IS" BASIS,
*  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
*  See the License for the specific language governing permissions and
*  limitations under the License.
*
******************************************************************************/

package test

import (
	"context"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-gorp/gorp/v3"
	"github.com/gophercloud/gophercloud/v2"
	. "github.com/majewsky/gg/option"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/mock"
	"github.com/sapcc/go-bits/osext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
	_ "github.com/sapcc/limes/internal/test/plugins"
)

type setupParams struct {
	DBSetupOptions           []easypg.TestSetupOption
	DBFixtureFile            string
	ConfigYAML               string
	APIBuilder               func(*core.Cluster, *gorp.DbMap, gopherpolicy.Validator, audittools.Auditor, func() time.Time, func() string) httpapi.API
	APIMiddlewares           []httpapi.API
	Projects                 []*core.KeystoneProject
	WithEmptyRecordsAsNeeded bool
}

// SetupOption is an option that can be given to NewSetup().
type SetupOption func(*setupParams)

// WithDBFixtureFile is a SetupOption that prefills the test DB by executing
// the SQL statements in the given file.
func WithDBFixtureFile(file string) SetupOption {
	return func(params *setupParams) {
		params.DBSetupOptions = append(params.DBSetupOptions, easypg.LoadSQLFile(file))
	}
}

// WithConfig is a SetupOption that initializes the test cluster from a
// configuration provided as YAML. This option is effectively required, as an
// empty cluster configuration is not allowed.
func WithConfig(yamlStr string) SetupOption {
	return func(params *setupParams) {
		params.ConfigYAML = normalizeInlineYAML(yamlStr)
	}
}

// WithAPIHandler is a SetupOption that initializes a http.Handler with the
// Limes API. The `apiBuilder` function signature matches NewV1API(). We cannot
// directly call this function because that would create an import cycle, so it
// must be given by the caller here.
func WithAPIHandler(apiBuilder func(*core.Cluster, *gorp.DbMap, gopherpolicy.Validator, audittools.Auditor, func() time.Time, func() string) httpapi.API, middlewares ...httpapi.API) SetupOption {
	return func(params *setupParams) {
		params.APIBuilder = apiBuilder
		params.APIMiddlewares = middlewares
	}
}

// WithProjects is a SetupOption that creates a DB entry for the given project.
// This also creates the corresponding domain object.
func WithProject(p core.KeystoneProject) SetupOption {
	return func(params *setupParams) {
		params.Projects = append(params.Projects, &p)
	}
}

// WithEmptyRecordsAsNeeded is a SetupOption that populates the DB with empty
// records for project_services, project_resources and project_az_resources.
func WithEmptyRecordsAsNeeded(params *setupParams) {
	params.WithEmptyRecordsAsNeeded = true
}

func normalizeInlineYAML(yamlStr string) string {
	// In the source code, we usually use tabs for YAML indentation because the
	// code is indented with tabs, and mixed indentation confuses some editors.
	// But YAML insists on using spaces for indentation.
	return strings.ReplaceAll(yamlStr, "\t", "  ")
}

// Setup contains all the pieces that are needed for most tests.
type Setup struct {
	// fields that are always set
	Ctx            context.Context //nolint:containedctx // only used in tests
	DB             *gorp.DbMap
	Cluster        *core.Cluster
	Clock          *mock.Clock
	Registry       *prometheus.Registry
	TokenValidator *mock.Validator[*PolicyEnforcer]
	Auditor        *audittools.MockAuditor
	// fields that are only set if their respective SetupOptions are given
	Handler http.Handler
	// fields that are filled by WithProject and WithEmptyRecordsAsNeeded
	Projects           []*db.Project
	ProjectServices    []*db.ProjectService
	ProjectResources   []*db.ProjectResource
	ProjectAZResources []*db.ProjectAZResource
}

func GenerateDummyToken() string {
	return "dummyToken"
}

// NewSetup prepares most or all pieces of Limes for a test.
func NewSetup(t *testing.T, opts ...SetupOption) Setup {
	logg.ShowDebug = osext.GetenvBool("LIMES_DEBUG")
	var params setupParams
	for _, option := range opts {
		option(&params)
	}

	var s Setup
	s.Ctx = t.Context()
	s.DB = initDatabase(t, params.DBSetupOptions)
	s.Cluster = initCluster(t, s.Ctx, params.ConfigYAML)
	s.Clock = mock.NewClock()
	s.Registry = prometheus.NewPedanticRegistry()

	// load mock policy (where everything is allowed)
	enforcer := &PolicyEnforcer{
		AllowCluster:      true,
		AllowDomain:       true,
		AllowProject:      true,
		AllowView:         true,
		AllowEdit:         true,
		AllowEditMaxQuota: true,
		AllowUncommit:     true,
	}
	mockUserIdentity := map[string]string{
		"user_id":             "uuid-for-alice",
		"user_name":           "alice",
		"user_domain_name":    "Default",
		"user_domain_id":      "uuid-for-default",
		"project_id":          "uuid-for-admin",
		"project_name":        "admin",
		"project_domain_name": "Default",
		"project_domain_id":   "uuid-for-default",
	}
	s.TokenValidator = mock.NewValidator(enforcer, mockUserIdentity)
	s.Auditor = audittools.NewMockAuditor()

	if params.APIBuilder != nil {
		s.Handler = httpapi.Compose(
			append([]httpapi.API{
				params.APIBuilder(s.Cluster, s.DB, s.TokenValidator, s.Auditor, s.Clock.Now, GenerateDummyToken),
				httpapi.WithoutLogging(),
			}, params.APIMiddlewares...)...,
		)
	}

	for idx, project := range params.Projects {
		dbDomain := &db.Domain{
			ID:   db.DomainID(idx),
			Name: "domain-" + strconv.Itoa(idx+1),
			UUID: "uuid-for-domain-" + strconv.Itoa(idx+1),
		}
		mustDo(t, s.DB.Insert(dbDomain))
		dbProject := &db.Project{
			ID:         db.ProjectID(idx),
			DomainID:   dbDomain.ID,
			Name:       project.Name,
			UUID:       project.UUID,
			ParentUUID: dbDomain.UUID,
		}
		mustDo(t, s.DB.Insert(dbProject))
		s.Projects = append(s.Projects, dbProject)
	}

	// fills all ProjectService entries (for each pair of project and service type),
	// all ProjectResource entries (for each pair of service and resource name),
	// all ProjectAZResource entries (for each pair of resource and AZ according to topology)
	if params.WithEmptyRecordsAsNeeded {
		if len(params.Projects) == 0 {
			t.Fatal("can not create empty DB records since there are no projects")
		}
		for _, dbProject := range s.Projects {
			for serviceType := range s.Cluster.Config.Liquids {
				t0 := time.Unix(0, 0).UTC()
				dbProjectService := &db.ProjectService{
					ID:             db.ProjectServiceID(len(s.ProjectServices) + 1),
					ProjectID:      dbProject.ID,
					Type:           serviceType,
					ScrapedAt:      Some(t0),
					RatesScrapedAt: Some(t0),
					CheckedAt:      Some(t0),
					RatesCheckedAt: Some(t0),
				}
				mustDo(t, s.DB.Insert(dbProjectService))
				s.ProjectServices = append(s.ProjectServices, dbProjectService)
				resInfos := s.Cluster.LiquidConnections[serviceType].ServiceInfo().Resources
				for _, resName := range slices.Sorted(maps.Keys(resInfos)) {
					dbProjectResource := &db.ProjectResource{
						ID:           db.ProjectResourceID(len(s.ProjectResources) + 1),
						ServiceID:    dbProjectService.ID,
						Name:         resName,
						Quota:        Some[uint64](0),
						BackendQuota: Some[int64](0),
					}
					mustDo(t, s.DB.Insert(dbProjectResource))
					s.ProjectResources = append(s.ProjectResources, dbProjectResource)
					var allAZs []liquid.AvailabilityZone
					if resInfos[resName].Topology == liquid.FlatTopology {
						allAZs = []liquid.AvailabilityZone{liquid.AvailabilityZoneAny}
					} else {
						allAZs = s.Cluster.Config.AvailabilityZones
					}
					for _, az := range allAZs {
						dbProjectAZResource := &db.ProjectAZResource{
							ID:               db.ProjectAZResourceID(len(s.ProjectAZResources) + 1),
							ResourceID:       dbProjectResource.ID,
							AvailabilityZone: az,
							Quota:            Some[uint64](0),
							Usage:            0,
							PhysicalUsage:    None[uint64](),
							SubresourcesJSON: "{}",
						}
						mustDo(t, s.DB.Insert(dbProjectAZResource))
						s.ProjectAZResources = append(s.ProjectAZResources, dbProjectAZResource)
					}
				}
			}
		}
	}

	return s
}

func mustDo(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err.Error())
	}
}

func initDatabase(t *testing.T, extraOpts []easypg.TestSetupOption) *gorp.DbMap {
	opts := append(slices.Clone(extraOpts),
		easypg.ClearTables("project_commitments", "cluster_services", "domains"),
		easypg.ResetPrimaryKeys(
			"cluster_services", "cluster_resources", "cluster_az_resources",
			"domains", "projects", "project_commitments", "project_mail_notifications",
			"project_services", "project_resources", "project_az_resources",
		),
	)
	return db.InitORM(easypg.ConnectForTest(t, db.Configuration(), opts...))
}

func initCluster(t *testing.T, ctx context.Context, configYAML string) *core.Cluster {
	cluster, errs := core.NewClusterFromYAML([]byte(configYAML))
	if errs.IsEmpty() {
		errs = cluster.Connect(ctx, nil, gophercloud.EndpointOpts{})
	}
	for _, err := range errs {
		t.Error(err)
	}
	if t.Failed() {
		t.FailNow()
	}
	return cluster
}
