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
	"regexp"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/go-gorp/gorp/v3"
	"github.com/gophercloud/gophercloud/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/errext"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/mock"
	"github.com/sapcc/go-bits/osext"

	"github.com/sapcc/limes/internal/api"
	"github.com/sapcc/limes/internal/collector"
	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

type setupParams struct {
	ConfigJSON               string
	APIMiddlewares           []httpapi.API
	WithInitialDiscovery     bool
	WithEmptyRecordsAsNeeded bool
	WithLiquidConnections    bool
	PersistedServiceInfo     map[db.ServiceType]liquid.ServiceInfo
	LiquidClients            map[db.ServiceType]*MockLiquidClient
}

// SetupOption is an option that can be given to NewSetup().
type SetupOption func(*setupParams)

// WithConfig is a SetupOption that initializes the test cluster from a
// configuration provided as JSON. This option is effectively required, as an
// empty cluster configuration is not allowed.
func WithConfig(jsonStr string) SetupOption {
	return func(params *setupParams) {
		params.ConfigJSON = RemoveCommentsFromJSON(jsonStr)
	}
}

// RemoveCommentsFromJSON removes C-style comments from JSON literals.
// It is intended only for use with JSON literals that appear in test code.
// Its implementation is very simple and not intended for use with untrusted inputs.
func RemoveCommentsFromJSON(jsonStr string) string {
	singleLineCommentRegex := regexp.MustCompile(`//[^\n]*`)
	multiLineCommentRegex := regexp.MustCompile(`(?s)/\*.*?\*/`)
	emptyLineRegex := regexp.MustCompile(`\n\s*\n`)

	result := singleLineCommentRegex.ReplaceAllString(jsonStr, "")
	result = multiLineCommentRegex.ReplaceAllString(result, "")
	result = emptyLineRegex.ReplaceAllString(result, "\n")
	return result
}

// WithAPIMiddleware is a SetupOption that attaches a custom middleware to the
// HTTP handler providing the Limes API within the test.
func WithAPIMiddleware(mw func(http.Handler) http.Handler) SetupOption {
	return func(params *setupParams) {
		params.APIMiddlewares = append(params.APIMiddlewares, httpapi.WithGlobalMiddleware(mw))
	}
}

// WithMockLiquidClient is a SetupOption that adds a MockLiquidClient to this test.
// This option must be provided once for every service type in the config.
func WithMockLiquidClient(serviceType db.ServiceType, serviceInfo liquid.ServiceInfo) SetupOption {
	return func(params *setupParams) {
		client := &MockLiquidClient{}
		client.ServiceInfo.Set(serviceInfo)
		params.LiquidClients[serviceType] = client
	}
}

// WithLiquidConnections is a SetupOption that sets up the Cluster the same way
// as the limes-collect task would do. This means a) the LiquidConnections are filled
// and b) the respective `services`, `resources` and `az_resources` records are
// persisted to the database.
func WithLiquidConnections(params *setupParams) {
	params.WithLiquidConnections = true
}

// WithPersistedServiceInfo is a SetupOption that fills ServiceInfo into the DB before setting up the Cluster instance.
// This is used to test how Cluster.Connect() reacts to preexisting metadata in the DB.
//
// Most tests will want to use EITHER this OR test.WithLiquidConnections().
// Either method will fill the `services`, `resources` and `az_resources` tables.
func WithPersistedServiceInfo(st db.ServiceType, si liquid.ServiceInfo) SetupOption {
	return func(params *setupParams) {
		params.PersistedServiceInfo[st] = si
	}
}

// WithEmptyRecordsAsNeeded is a SetupOption that populates the DB with empty
// records for project_resources and project_az_resources.
//
// It relies on the services, resources and az_resources to exist!
// (e.g. use WithPersistedServiceInfo)
//
// It also relies on domains and projects to exist!
// (e.g. use WithInitialDiscovery)
func WithEmptyRecordsAsNeeded(params *setupParams) {
	params.WithEmptyRecordsAsNeeded = true
}

// WithInitialDiscovery is a SetupOption that populates the DB with records for
// domains, projects and project_services using the data in the config section
// "discovery.static_config", by running the ScanDomainsAndProjectsJob.
func WithInitialDiscovery(params *setupParams) {
	params.WithInitialDiscovery = true
}

// Setup contains all the pieces that are needed for most tests.
type Setup struct {
	// fields that are always set
	Ctx                        context.Context //nolint:containedctx // only used in tests
	DB                         *gorp.DbMap
	Cluster                    *core.Cluster
	Clock                      *mock.Clock
	Registry                   *prometheus.Registry
	TokenValidator             *mock.Validator[*PolicyEnforcer]
	Auditor                    *audittools.MockAuditor
	LiquidClients              map[db.ServiceType]*MockLiquidClient
	Handler                    http.Handler
	CurrentProjectCommitmentID *uint64
	CurrentTransferTokenNumber *uint64
	Collector                  *collector.Collector

	// for t.Fatal() in helper functions
	t *testing.T
}

// GenerateDummyTransferToken generates a token string from an ID for testing.
func GenerateDummyTransferToken(idx uint64) string {
	return "dummyToken-" + strconv.FormatUint(idx, 10)
}

func transferTokenGenerator() (generator func() string, currentTransferTokenNumber *uint64) {
	idx := uint64(0)
	return func() string {
		idx++
		return GenerateDummyTransferToken(idx)
	}, &idx
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

	// validate selected options
	if params.WithEmptyRecordsAsNeeded && !params.WithInitialDiscovery {
		t.Fatal("can not create empty DB records since no projects are being discovered during setup")
	}

	var s Setup
	s.Ctx = t.Context()
	s.Clock = mock.NewClock()
	s.t = t

	s.DB = db.InitORM(easypg.ConnectForTest(t, db.Configuration(),
		easypg.ClearTables("project_commitments", "services", "domains"),
		easypg.ResetPrimaryKeys(
			"services", "resources", "rates", "az_resources",
			"domains", "projects", "project_commitments", "project_mail_notifications",
			"project_services", "project_resources", "project_az_resources", "project_rates",
		),
	))

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
	s.Cluster, errs = core.NewClusterFromJSON([]byte(params.ConfigJSON), s.Clock.Now, s.DB, params.WithLiquidConnections)
	failIfErrs(t, errs)

	// persistedServiceInfo is saved to the DB first, so that Cluster.Connect can be checked with it
	for _, serviceType := range slices.Sorted(maps.Keys(params.PersistedServiceInfo)) {
		// handle non-configured services for creation of "orphaned" db entries
		serviceInfo := params.PersistedServiceInfo[serviceType]
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

	projectCommitmentUUIDGenerator, currentProjectCommitmentID := projectCommitmentUUIDGenerator()
	transferTokenGenerator, currentTransferTokenNumber := transferTokenGenerator()
	s.CurrentProjectCommitmentID = currentProjectCommitmentID
	s.CurrentTransferTokenNumber = currentTransferTokenNumber
	s.Handler = httpapi.Compose(
		append(params.APIMiddlewares,
			api.NewV1API(s.Cluster, s.TokenValidator, s.Auditor, s.Clock.Now, transferTokenGenerator, projectCommitmentUUIDGenerator),
			httpapi.WithoutLogging(),
		)...,
	)

	s.Collector = &collector.Collector{
		Cluster:     s.Cluster,
		DB:          s.DB,
		LogError:    t.Errorf,
		MeasureTime: s.Clock.Now,
		MeasureTimeAtEnd: func() time.Time {
			s.Clock.StepBy(5 * time.Second)
			return s.Clock.Now()
		},
		AddJitter:                     NoJitter,
		GenerateProjectCommitmentUUID: projectCommitmentUUIDGenerator,
		GenerateTransferToken:         transferTokenGenerator,
	}

	if params.WithInitialDiscovery {
		_, err := s.Collector.ScanDomains(s.Ctx, collector.ScanDomainsOpts{ScanAllProjects: true})
		if err != nil {
			t.Fatal(err.Error())
		}
	}

	if params.WithEmptyRecordsAsNeeded {
		// fills all ProjectResource entries (for each pair of service and resource name)
		// and all ProjectAZResource entries (for each pair of resource and AZ according to topology)
		s.MustDBExec(db.ExpandEnumPlaceholders(`
			WITH tmp AS (
				SELECT r.id AS id, CASE
					WHEN NOT r.has_quota THEN NULL
					WHEN r.topology = {{liquid.AZSeparatedTopology}} THEN NULL
					ELSE 0
				END AS default_quota FROM resources r
			)
			INSERT INTO project_resources (project_id, resource_id, quota, backend_quota) SELECT
				p.id              AS project_id,
				tmp.id            AS resource_id,
				tmp.default_quota AS quota,
				tmp.default_quota AS backend_quota
			FROM tmp CROSS JOIN projects p ORDER BY p.id, tmp.id
		`))

		s.MustDBExec(db.ExpandEnumPlaceholders(`
			WITH tmp AS (
				SELECT azr.id AS id, CASE
					WHEN NOT r.has_quota THEN NULL
					WHEN r.topology != {{liquid.AZSeparatedTopology}} THEN NULL
					WHEN azr.az = {{liquid.AvailabilityZoneUnknown}} THEN NULL
					ELSE 0
				END AS default_quota FROM az_resources azr JOIN resources r ON azr.resource_id = r.id
			)
			INSERT INTO project_az_resources (project_id, az_resource_id, quota, usage, subresources) SELECT
				p.id              AS project_id,
				tmp.id            AS az_resource_id,
				tmp.default_quota AS quota,
				0                 AS usage,
				''                AS subresources
			FROM tmp CROSS JOIN projects p ORDER BY p.id, tmp.id
		`))
	}

	return s
}

// GetServiceID is a helper function for finding the ID of a db.Service record.
func (s Setup) GetServiceID(srvType db.ServiceType) (result db.ServiceID) {
	s.t.Helper()
	err := s.DB.QueryRow(`SELECT id FROM services WHERE type = $1`, srvType).Scan(&result)
	if err != nil {
		s.t.Fatalf("could not find services.id for type = %q: %s", srvType, err.Error())
	}
	return result
}

// GetResourceID is a helper function for finding the ID of a db.Resource record.
func (s Setup) GetResourceID(srvType db.ServiceType, resName liquid.ResourceName) (result db.ResourceID) {
	s.t.Helper()
	path := string(srvType) + "/" + string(resName)
	err := s.DB.QueryRow(`SELECT id FROM resources WHERE path = $1`, path).Scan(&result)
	if err != nil {
		s.t.Fatalf("could not find resources.id for path = %q: %s", path, err.Error())
	}
	return result
}

// GetAZResourceID is a helper function for finding the ID of a db.AZResource record.
func (s Setup) GetAZResourceID(srvType db.ServiceType, resName liquid.ResourceName, az limes.AvailabilityZone) (result db.AZResourceID) {
	s.t.Helper()
	path := string(srvType) + "/" + string(resName) + "/" + string(az)
	err := s.DB.QueryRow(`SELECT id FROM az_resources WHERE path = $1`, path).Scan(&result)
	if err != nil {
		s.t.Fatalf("could not find az_resources.id for path = %q: %s", path, err.Error())
	}
	return result
}

// GetRateID is a helper function for finding the ID of a db.Rate record.
func (s Setup) GetRateID(srvType db.ServiceType, rateName liquid.RateName) (result db.RateID) {
	// TODO: we should have a `path` attribute on `rates`, too
	s.t.Helper()
	path := string(srvType) + "/" + string(rateName)
	err := s.DB.QueryRow(`SELECT id FROM rates WHERE service_id = $1 AND name = $2`, s.GetServiceID(srvType), rateName).Scan(&result)
	if err != nil {
		s.t.Fatalf("could not find rates.id for path = %q: %s", path, err.Error())
	}
	return result
}

// GetProjectID is a helper function for finding the ID of a db.Project record.
func (s Setup) GetProjectID(name string) (result db.ProjectID) {
	s.t.Helper()
	err := s.DB.QueryRow(`SELECT id FROM projects WHERE name = $1`, name).Scan(&result)
	if err != nil {
		s.t.Fatalf("could not find projects.id for name = %q: %s", name, err.Error())
	}
	return result
}

// MustDBExec is a shorthand for s.DB.Exec() + t.Fatal() on error.
func (s Setup) MustDBExec(query string, args ...any) {
	s.t.Helper()
	_, err := s.DB.Exec(query, args...)
	if err != nil {
		s.t.Fatal(err.Error())
	}
}

// MustDBInsert is a shorthand for s.DB.Insert() + t.Fatal() on error.
func (s Setup) MustDBInsert(pointerToRecord any) {
	s.t.Helper()
	err := s.DB.Insert(pointerToRecord)
	if err != nil {
		s.t.Fatal(err.Error())
	}
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
