// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/httptest"
	"github.com/sapcc/go-bits/must"
	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/test"
)

func TestV2ResourcesAvailability(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(resourceReportConfigJSON),
		test.WithPersistedServiceInfo("first", test.DefaultLiquidServiceInfo("First")),
		test.WithPersistedServiceInfo("second", test.DefaultLiquidServiceInfo("Second")),
		test.WithInitialDiscovery,
		test.WithEmptyResourceRecordsAsNeeded,
	)
	fixturePath := "./fixtures/resource-availability.json"

	s.Clock.StepBy(time.Hour)

	// Set up capacity and usage data.
	// "first/capacity" has overcommit_factor=2, so effective capacity = 2*raw_capacity.
	// raw_capacity=1000 per AZ => effective capacity=2000 per AZ.
	s.MustDBExec(`UPDATE az_resources SET raw_capacity = 1000, usage = 500 WHERE path LIKE 'first/capacity/az-%'`)
	// "second/capacity" has no overcommit, raw_capacity=500 per AZ => effective capacity=500.
	s.MustDBExec(`UPDATE az_resources SET raw_capacity = 500 WHERE path LIKE 'second/capacity/az-%'`)
	// Set usage on project_az_resources (3 projects, each gets 10 usage for capacity in each AZ)
	s.MustDBExec(`UPDATE project_az_resources SET usage = 10 WHERE az_resource_id IN (SELECT id FROM az_resources WHERE path LIKE '%/capacity/%')`)
	// Set usage for "things" (flat topology, has_capacity=false → excluded)
	s.MustDBExec(`UPDATE project_az_resources SET usage = 5 WHERE az_resource_id IN (SELECT id FROM az_resources WHERE path LIKE '%/things/%')`)

	// Create some confirmed commitment to test that they count towards allocation.
	// Berlin: 100 confirmed in first/capacity/az-one
	berlin := s.GetProjectID("berlin")
	firstCapacityAZOne := s.GetAZResourceID("first", "capacity", "az-one")
	firstCapacityAZTwo := s.GetAZResourceID("first", "capacity", "az-two")
	s.MustDBInsert(&db.ProjectCommitment{
		UUID:                test.GenerateDummyCommitmentUUID(1),
		ProjectID:           berlin,
		AZResourceID:        firstCapacityAZOne,
		Amount:              100,
		Duration:            must.ReturnT(limesresources.ParseCommitmentDuration("1 year"))(t),
		CreatedAt:           s.Clock.Now(),
		UpdatedAt:           s.Clock.Now(),
		CreatorUUID:         "dummy",
		CreatorName:         "dummy",
		ConfirmedAt:         Some(s.Clock.Now()),
		ExpiresAt:           s.Clock.Now().AddDate(1, 0, 0),
		Status:              liquid.CommitmentStatusConfirmed,
		CreationContextJSON: json.RawMessage(`{}`),
	})
	// Berlin: 50 pending in first/capacity/az-two → should NOT count towards allocation
	s.MustDBInsert(&db.ProjectCommitment{
		UUID:                test.GenerateDummyCommitmentUUID(2),
		ProjectID:           berlin,
		AZResourceID:        firstCapacityAZTwo,
		Amount:              50,
		Duration:            must.ReturnT(limesresources.ParseCommitmentDuration("1 year"))(t),
		CreatedAt:           s.Clock.Now(),
		UpdatedAt:           s.Clock.Now(),
		CreatorUUID:         "dummy",
		CreatorName:         "dummy",
		ExpiresAt:           s.Clock.Now().AddDate(1, 0, 0),
		Status:              liquid.CommitmentStatusPending,
		CreationContextJSON: json.RawMessage(`{}`),
	})

	// Permission check
	s.TokenValidator.Enforcer.AllowReportAvailability = false
	s.Handler.RespondTo(s.Ctx, "GET /v2/resources/availability").
		ExpectText(t, http.StatusForbidden, "Forbidden\n")
	s.TokenValidator.Enforcer.AllowReportAvailability = true

	// Cloud-admin: sees all resources with capacity (things has no capacity!)
	s.Handler.RespondTo(s.Ctx, "GET /v2/resources/availability").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "cloud-admin"))

	// Domain-scoped token for germany: sees only resources not forbidden in at least one project of that domain
	s.TokenValidator.Enforcer.AllowCluster = false
	s.UpdateMockUserIdentity(map[string]string{
		"project_id": "", "project_domain_id": "", "project_name": "", "project_domain_name": "",
		"domain_id": "uuid-for-germany", "domain_name": "germany",
	})
	s.Handler.RespondTo(s.Ctx, "GET /v2/resources/availability").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "domain-germany"))

	// Make first/capacity forbidden for all projects in germany
	firstCapacityResourceID := s.GetResourceID("first", "capacity")
	s.MustDBExec(`UPDATE project_resources SET forbidden = true WHERE resource_id = $1 AND project_id IN (SELECT id FROM projects WHERE domain_id = (SELECT id FROM domains WHERE name = 'germany'))`, firstCapacityResourceID)
	s.Handler.RespondTo(s.Ctx, "GET /v2/resources/availability").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "domain-germany-forbidden").
			Modify("del(.service_areas.first)"))

	// allow it for one project - it appears again in the report
	s.MustDBExec(`UPDATE project_resources SET forbidden = false WHERE resource_id = $1 AND project_id = $2`, firstCapacityResourceID, berlin)
	s.Handler.RespondTo(s.Ctx, "GET /v2/resources/availability").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "domain-germany-partial-forbidden"))

	// Undo forbidden
	s.MustDBExec(`UPDATE project_resources SET forbidden = false WHERE resource_id = $1`, firstCapacityResourceID)

	// Project-scoped token for berlin: sees resources allowed for that project
	s.TokenValidator.Enforcer.AllowDomain = false
	s.UpdateMockUserIdentity(map[string]string{
		"domain_id": "", "domain_name": "",
		"project_id": "uuid-for-berlin", "project_name": "berlin", "project_domain_id": "uuid-for-germany", "project_domain_name": "germany",
	})
	s.Handler.RespondTo(s.Ctx, "GET /v2/resources/availability").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "project-berlin"))

	// Make first/capacity forbidden for berlin
	s.MustDBExec(`UPDATE project_resources SET forbidden = true WHERE resource_id = $1 AND project_id = $2`, firstCapacityResourceID, berlin)
	s.Handler.RespondTo(s.Ctx, "GET /v2/resources/availability").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "project-berlin-forbidden").
			Modify("del(.service_areas.first)"))
}
