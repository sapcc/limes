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
	"github.com/sapcc/limes/internal/util"
)

func TestCommitmentGetSingle(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(commitmentCreateConfigJSON),
		test.WithPersistedServiceInfo("first", test.DefaultLiquidServiceInfo("First")),
		test.WithPersistedServiceInfo("second", test.DefaultLiquidServiceInfo("Second")),
		test.WithInitialDiscovery,
		test.WithEmptyResourceRecordsAsNeeded,
	)

	// setup: place one commitment into the DB
	projectParisID := s.GetProjectID("paris")
	firstCapacityID := s.GetAZResourceID("first", "capacity", "az-one")
	expiresAt := s.Clock.Now().AddDate(1, 0, 0).UTC()
	uuidOne := liquid.CommitmentUUID("00000000-0000-0000-0000-000000000001")
	s.MustDBInsert(&db.ProjectCommitment{
		UUID:                uuidOne,
		ProjectID:           projectParisID,
		AZResourceID:        firstCapacityID,
		Amount:              10,
		Duration:            must.Return(limesresources.ParseCommitmentDuration("1 year")),
		CreatedAt:           s.Clock.Now(),
		UpdatedAt:           s.Clock.Now(),
		ConfirmedAt:         Some(s.Clock.Now()),
		CreatorUUID:         "dummy",
		CreatorName:         "dummy",
		ExpiresAt:           expiresAt,
		CreationContextJSON: must.Return(json.Marshal(db.CommitmentWorkflowContext{Reason: db.CommitmentReasonCreate})),
		Status:              liquid.CommitmentStatusConfirmed,
	})
	s.Clock.StepBy(time.Hour)

	// success: get existing commitment
	fixturePath := "./fixtures/commitment-get-single.json"
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments/00000000-0000-0000-0000-000000000001").
		ExpectJSON(t, http.StatusOK,
			httptest.NewJQModifiableJSONFixture(fixturePath, "success"))

	// error: get non-existing commitment
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments/00000000-0000-0000-0000-000000000099").
		ExpectText(t, http.StatusNotFound, "no such commitment\n")

	// error: permission denied
	s.TokenValidator.Enforcer.AllowCommitmentGet = false
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments/00000000-0000-0000-0000-000000000001").
		ExpectText(t, http.StatusForbidden, "Forbidden\n")
	s.TokenValidator.Enforcer.AllowCommitmentGet = true

	// error: permission denied as regular user for deleted commitment
	s.TokenValidator.Enforcer.AllowCommitmentGetAdmin = false
	s.MustDBExec(`UPDATE project_commitments SET status = 'deleted' WHERE uuid = $1`, uuidOne)
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments/00000000-0000-0000-0000-000000000001").
		ExpectText(t, http.StatusForbidden, "Forbidden\n")
	s.TokenValidator.Enforcer.AllowCommitmentGetAdmin = true

	// success: admin user can see deleted commitment
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments/00000000-0000-0000-0000-000000000001").
		ExpectJSON(t, http.StatusOK,
			httptest.NewJQModifiableJSONFixture(fixturePath, "success").
				Modify(`.status = "deleted"`))
}

func TestCommitmentGetMultiple(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(commitmentCreateConfigJSON),
		test.WithPersistedServiceInfo("first", test.DefaultLiquidServiceInfo("First")),
		test.WithPersistedServiceInfo("second", test.DefaultLiquidServiceInfo("Second")),
		test.WithInitialDiscovery,
		test.WithEmptyResourceRecordsAsNeeded,
	)

	// setup: place commitments into the DB
	projectParisID := s.GetProjectID("paris")
	projectBerlinID := s.GetProjectID("berlin")
	projectDresdenID := s.GetProjectID("dresden")
	firstCapacityID := s.GetAZResourceID("first", "capacity", "az-one")
	expiresAt := s.Clock.Now().AddDate(1, 0, 0).UTC()

	testCommitment := &db.ProjectCommitment{
		UUID:                "00000000-0000-0000-0000-000000000001",
		ProjectID:           projectParisID,
		AZResourceID:        firstCapacityID,
		Amount:              10,
		Duration:            must.Return(limesresources.ParseCommitmentDuration("1 year")),
		CreatedAt:           s.Clock.Now(),
		UpdatedAt:           s.Clock.Now(),
		ConfirmedAt:         Some(s.Clock.Now()),
		CreatorUUID:         "dummy",
		CreatorName:         "dummy",
		ExpiresAt:           expiresAt,
		CreationContextJSON: must.Return(json.Marshal(db.CommitmentWorkflowContext{Reason: db.CommitmentReasonCreate})),
		Status:              liquid.CommitmentStatusConfirmed,
	}
	s.MustDBInsert(testCommitment)
	// one more regular one
	testCommitment.ID = 0
	testCommitment.UUID = "00000000-0000-0000-0000-000000000002"
	testCommitment.ProjectID = projectBerlinID
	testCommitment.Amount = 20
	s.MustDBInsert(testCommitment)
	// a public commitment
	s.Clock.StepBy(time.Hour)
	testCommitment.CreatedAt = s.Clock.Now()
	testCommitment.UpdatedAt = s.Clock.Now()
	testCommitment.ExpiresAt = s.Clock.Now().AddDate(1, 0, 0).UTC()
	testCommitment.ID = 0
	testCommitment.UUID = "00000000-0000-0000-0000-000000000003"
	testCommitment.ProjectID = projectDresdenID
	testCommitment.Amount = 5
	testCommitment.TransferStatus = limesresources.CommitmentTransferStatusPublic
	testCommitment.TransferToken = Some(test.GenerateDummyTransferToken(1))
	s.MustDBInsert(testCommitment)
	// a deleted commitment
	testCommitment.ID = 0
	testCommitment.UUID = "00000000-0000-0000-0000-000000000004"
	testCommitment.TransferStatus = limesresources.CommitmentTransferStatusNone
	testCommitment.TransferToken = None[string]()
	testCommitment.Status = util.CommitmentStatusDeleted
	s.MustDBInsert(testCommitment)
	s.Clock.StepBy(time.Hour)

	// To make the token project-scoped to "paris" in domain "france"
	s.UpdateMockUserIdentity(map[string]string{
		"project_id":          "uuid-for-paris",
		"project_name":        "paris",
		"project_domain_name": "france",
		"project_domain_id":   "uuid-for-france",
	})

	// error cases
	// error: no main filter set (admin)
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments").
		ExpectText(t, http.StatusBadRequest, "one of \"category\" or \"resource\" must be set\n")

	// error: no main filter set (non-admin)
	s.TokenValidator.Enforcer.AllowCommitmentGetAdmin = false
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments").
		ExpectText(t, http.StatusBadRequest, "one of \"public\", \"project_uuid\", \"domain_uuid\" must be set\n")

	// error: multiple main filters set
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments?public=true&project_uuid=uuid-for-paris").
		ExpectText(t, http.StatusBadRequest, "only one of \"public\", \"project_uuid\", \"domain_uuid\" may be set\n")

	// error: category without service
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments?project_uuid=uuid-for-paris&category=foo").
		ExpectText(t, http.StatusBadRequest, "\"category\" or \"resource\" require \"service\" to be set\n")

	// error: resource without service
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments?project_uuid=uuid-for-paris&resource=capacity").
		ExpectText(t, http.StatusBadRequest, "\"category\" or \"resource\" require \"service\" to be set\n")

	// error: with=deleted without admin
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments?project_uuid=uuid-for-paris&with=deleted").
		ExpectText(t, http.StatusForbidden, "\"with=deleted\" requires special permissions\n")

	// error: no permission at all
	s.TokenValidator.Enforcer.AllowCommitmentGet = false
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments?project_uuid=uuid-for-paris").
		ExpectText(t, http.StatusForbidden, "Forbidden\n")
	s.TokenValidator.Enforcer.AllowCommitmentGet = true

	// error: no permission for public
	s.TokenValidator.Enforcer.AllowCommitmentGetPublic = false
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments?public=true").
		ExpectText(t, http.StatusForbidden, "Forbidden\n")
	s.TokenValidator.Enforcer.AllowCommitmentGetPublic = true

	// --- Successful queries ---

	// success: filter by project_uuid
	fixturePath := "./fixtures/commitment-get-multiple.json"
	deletedModification := `del(.commitments.[] | select(.status == "deleted"))`
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments?project_uuid=uuid-for-paris").
		ExpectJSON(t, http.StatusOK,
			httptest.NewJQModifiableJSONFixture(fixturePath, "single project").
				Modify(deletedModification).
				Modify(`del(.commitments.[] | select(.project_id != "uuid-for-paris"))`))

	// success: filter by domain_uuid (switch to domain-scoped token)
	s.UpdateMockUserIdentity(map[string]string{
		"project_id":          "",
		"project_name":        "",
		"project_domain_name": "",
		"project_domain_id":   "",
		"domain_id":           "uuid-for-france",
		"domain_name":         "france",
	})
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments?domain_uuid=uuid-for-germany").
		ExpectJSON(t, http.StatusOK,
			httptest.NewJQModifiableJSONFixture(fixturePath, "single domain").
				Modify(deletedModification).
				Modify(`del(.commitments.[] | select(.project_id == "uuid-for-paris"))`))

	// success: filter by public
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments?public=true").
		ExpectJSON(t, http.StatusOK,
			httptest.NewJQModifiableJSONFixture(fixturePath, "public").
				Modify(deletedModification).
				Modify(`del(.commitments.[] | select(.uuid != "00000000-0000-0000-0000-000000000003"))`))

	// success: with service filter (admin scenario — switch to cluster-level token)
	s.TokenValidator.Enforcer.AllowCommitmentGetAdmin = true
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments?service=first").
		ExpectJSON(t, http.StatusOK,
			httptest.NewJQModifiableJSONFixture(fixturePath, "service filter").
				Modify(deletedModification))

	// success: deleted (and service filter, because it's always required)
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments?with=deleted&service=first").
		ExpectJSON(t, http.StatusOK,
			httptest.NewJQModifiableJSONFixture(fixturePath, "deleted"))

	// success: updated after
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments?service=first&updated_after="+s.Clock.Now().Add(-1*time.Hour).Format(time.RFC3339)).
		ExpectJSON(t, http.StatusOK,
			httptest.NewJQModifiableJSONFixture(fixturePath, "updated_after").
				Modify(`del(.commitments.[] | select(.uuid != "00000000-0000-0000-0000-000000000003"))`))
}
