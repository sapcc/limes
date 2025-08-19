// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
	"github.com/sapcc/go-bits/errext"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/mock"
	"github.com/sapcc/go-bits/osext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

type setupParams struct {
	DBSetupOptions           []easypg.TestSetupOption
	DBFixtureFile            string
	ConfigYAML               string
	APIBuilder               func(*core.Cluster, gopherpolicy.Validator, audittools.Auditor, func() time.Time, func() string, func() liquid.CommitmentUUID) httpapi.API
	APIMiddlewares           []httpapi.API
	Projects                 []*core.KeystoneProject
	WithEmptyRecordsAsNeeded bool
	WithLiquidConnections    bool
	PersistedServiceInfo     map[db.ServiceType]liquid.ServiceInfo
	LiquidClients            map[db.ServiceType]*MockLiquidClient
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
func WithAPIHandler(apiBuilder func(*core.Cluster, gopherpolicy.Validator, audittools.Auditor, func() time.Time, func() string, func() liquid.CommitmentUUID) httpapi.API, middlewares ...httpapi.API) SetupOption {
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

// WithMockLiquidClient is a SetupOption that adds a MockLiquidClient to this test.
// This option must be provided once for every service type in the config.
func WithMockLiquidClient(serviceType db.ServiceType, serviceInfo liquid.ServiceInfo) SetupOption {
	return func(params *setupParams) {
		params.LiquidClients[serviceType] = &MockLiquidClient{serviceInfo: serviceInfo}
	}
}

// WithLiquidConnections is a SetupOption that sets up the Cluster the same way
// as the limes-collect task would do. This means a) the LiquidConnections are filled
// and b) persisted to the database.
func WithLiquidConnections(params *setupParams) {
	params.WithLiquidConnections = true
}

// WithPersistedServiceInfo is a SetupOption that fills ServiceInfo into the DB before setting up the Cluster instance.
// This is used to test how Cluster.Connect() reacts to preexisting metadata in the DB.
func WithPersistedServiceInfo(st db.ServiceType, si liquid.ServiceInfo) SetupOption {
	return func(params *setupParams) {
		params.PersistedServiceInfo[st] = si
	}
}

// WithEmptyRecordsAsNeeded is a SetupOption that populates the DB with empty
// records for project_services, project_resources and project_az_resources.
// It relies on the services, resources and az_resources
// to exist! (e.g. use WithPersistedServiceInfo)
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
	LiquidClients  map[db.ServiceType]*MockLiquidClient
	// fields that are only set if their respective SetupOptions are given
	Handler                    http.Handler
	CurrentProjectCommitmentID *uint64
	// fields that are filled by WithProject and WithEmptyRecordsAsNeeded
	Projects           []*db.Project
	ProjectServices    []*db.ProjectService
	ProjectResources   []*db.ProjectResource
	ProjectAZResources []*db.ProjectAZResource
}

func GenerateDummyToken() string {
	return "dummyToken"
}

// GenerateDummyCommitmentUUID generates a deterministic UUID from the given ID.
func GenerateDummyCommitmentUUID(idx uint64) liquid.CommitmentUUID {
	// e.g. idx = 5
	//   -> str = hex(sha256("5")) = "ef2d127de37b942baad06145e54b0c619a1f22327b2ebbcfbec78f5564afe39d"
	//   -> uuid = "ef2d127d-e37b-4942-baad-06145e54b0c6"
	buf := sha256.Sum256([]byte(strconv.FormatUint(idx, 10)))
	str := hex.EncodeToString(buf[:])
	uuid := fmt.Sprintf("%s-%s-4%s-%s-%s", str[0:8], str[8:12], str[13:16], str[16:20], str[20:32])
	return liquid.CommitmentUUID(uuid)
}

func projectCommitmentUUIDGenerator() (generator func() liquid.CommitmentUUID, currentProjectCommitmentID *uint64) {
	idx := uint64(0)
	return func() liquid.CommitmentUUID {
		idx++
		return GenerateDummyCommitmentUUID(idx)
	}, &idx
}

// NewSetup prepares most or all pieces of Limes for a test.
func NewSetup(t *testing.T, opts ...SetupOption) Setup {
	logg.ShowDebug = osext.GetenvBool("LIMES_DEBUG")
	params := setupParams{
		PersistedServiceInfo: make(map[db.ServiceType]liquid.ServiceInfo),
		LiquidClients:        make(map[db.ServiceType]*MockLiquidClient),
	}
	for _, option := range opts {
		option(&params)
	}

	var s Setup
	s.Ctx = t.Context()
	s.DB = initDatabase(t, params.DBSetupOptions)
	s.Clock = mock.NewClock()

	// Cluster.Connect() needs to use our MockLiquidClient instances instead of real LIQUID clients
	s.LiquidClients = params.LiquidClients
	liquidClientFactory := func(serviceType db.ServiceType) (core.LiquidClient, error) {
		client, ok := s.LiquidClients[serviceType]
		if !ok {
			return nil, fmt.Errorf("no MockLiquidClient registered for service type %q", serviceType)
		}
		return client, nil
	}

	// we need the Cluster for the availability zones, so create it first
	var errs errext.ErrorSet
	s.Cluster, errs = core.NewClusterFromYAML([]byte(params.ConfigYAML), s.Clock.Now, s.DB, params.WithLiquidConnections)
	failIfErrs(t, errs)

	// persistedServiceInfo is saved to the DB first, so that Cluster.Connect can be checked with it
	for serviceType, serviceInfo := range params.PersistedServiceInfo {
		// handle non-configured services for creation of "orphaned" db entries
		liquidConfig, exists := s.Cluster.Config.Liquids[serviceType]
		var rateLimits core.ServiceRateLimitConfiguration
		if exists {
			rateLimits = liquidConfig.RateLimits
		}
		_, err := core.SaveServiceInfoToDB(serviceType, serviceInfo, s.Cluster.Config.AvailabilityZones, rateLimits, s.Clock.Now(), s.DB)
		if err != nil {
			t.Fatal(err)
		}
	}
	errs = s.Cluster.Connect(s.Ctx, nil, gophercloud.EndpointOpts{}, liquidClientFactory)
	failIfErrs(t, errs)

	serviceInfos, err := s.Cluster.AllServiceInfos()
	if err != nil {
		t.Fatal(err)
	}

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
		generator, currentProjectCommitmentID := projectCommitmentUUIDGenerator()
		s.CurrentProjectCommitmentID = currentProjectCommitmentID
		s.Handler = httpapi.Compose(
			append([]httpapi.API{
				params.APIBuilder(s.Cluster, s.TokenValidator, s.Auditor, s.Clock.Now, GenerateDummyToken, generator),
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
	if !params.WithEmptyRecordsAsNeeded {
		return s
	}
	if len(params.Projects) == 0 {
		t.Fatal("can not create empty DB records since there are no projects")
	}
	for serviceType := range s.Cluster.Config.Liquids {
		var service *db.Service
		mustDo(t, s.DB.SelectOne(&service, `SELECT * FROM services WHERE type = $1`, serviceType))
		for _, dbProject := range s.Projects {
			t0 := time.Unix(0, 0).UTC()
			dbProjectService := &db.ProjectService{
				ID:        db.ProjectServiceID(len(s.ProjectServices) + 1),
				ProjectID: dbProject.ID,
				ServiceID: service.ID,
				ScrapedAt: Some(t0),
				CheckedAt: Some(t0),
			}
			mustDo(t, s.DB.Insert(dbProjectService))
			s.ProjectServices = append(s.ProjectServices, dbProjectService)
		}
		resInfos := core.InfoForService(serviceInfos, serviceType).Resources
		for _, resName := range slices.Sorted(maps.Keys(resInfos)) {
			var resource *db.Resource
			mustDo(t, s.DB.SelectOne(&resource, `SELECT * FROM resources WHERE name = $1 AND service_id = $2`, resName, service.ID))
			for _, dbProject := range s.Projects {
				dbProjectResource := &db.ProjectResource{
					ID:           db.ProjectResourceID(len(s.ProjectResources) + 1),
					ProjectID:    dbProject.ID,
					ResourceID:   resource.ID,
					Quota:        Some[uint64](0),
					BackendQuota: Some[int64](0),
				}
				mustDo(t, s.DB.Insert(dbProjectResource))
				s.ProjectResources = append(s.ProjectResources, dbProjectResource)
			}
			var allAZs []liquid.AvailabilityZone
			if resource.Topology == liquid.FlatTopology {
				allAZs = []liquid.AvailabilityZone{liquid.AvailabilityZoneAny}
			} else {
				allAZs = s.Cluster.Config.AvailabilityZones
			}
			for _, az := range allAZs {
				var azResource *db.AZResource
				mustDo(t, s.DB.SelectOne(&azResource, `SELECT * FROM az_resources WHERE az = $1 AND resource_id = $2`, az, resource.ID))
				for _, dbProject := range s.Projects {
					dbProjectAZResource := &db.ProjectAZResource{
						ID:               db.ProjectAZResourceID(len(s.ProjectAZResources) + 1),
						ProjectID:        dbProject.ID,
						AZResourceID:     azResource.ID,
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
		easypg.ClearTables("project_commitments", "services", "domains"),
		easypg.ResetPrimaryKeys(
			"services", "resources", "rates", "az_resources",
			"domains", "projects", "project_commitments", "project_mail_notifications",
			"project_services", "project_resources", "project_az_resources", "project_rates",
		),
	)
	return db.InitORM(easypg.ConnectForTest(t, db.Configuration(), opts...))
}

func failIfErrs(t *testing.T, errs errext.ErrorSet) {
	t.Helper()
	for _, err := range errs {
		t.Error(err)
	}
	if t.Failed() {
		t.FailNow()
	}
}
