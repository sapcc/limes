// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/sapcc/go-api-declarations/cadf"
	"github.com/sapcc/go-bits/httptest"
	. "go.xyrillian.de/gg/option"

	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/must"
	"go.xyrillian.de/gg/assert"

	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/test"
)

// Helper function for a successful commitment deletion.
func deleteCommitmentAndExpectSuccess(t *testing.T, s test.Setup, tr *easypg.Tracker, liquidHandlesCommitments bool, uuid liquid.CommitmentUUID, ccr liquid.CommitmentChangeRequest) {
	t.Helper()
	path := "/resources/v2/commitments/" + string(uuid)
	s.Handler.RespondTo(s.Ctx, "DELETE "+path).ExpectStatus(t, http.StatusNoContent)

	// assertions
	tr.DBChanges().AssertEqualf("UPDATE project_commitments SET status = 'deleted', deleted_at = %[1]d, updated_at = %[1]d WHERE id = 1 AND uuid = '00000000-0000-0000-0000-000000000001' AND transfer_token = NULL;", s.Clock.Now().Unix())
	s.Auditor.ExpectEvents(t, cadf.Event{
		Action:      "delete",
		Outcome:     "success",
		Reason:      cadf.Reason{ReasonType: "HTTP", ReasonCode: "204"},
		RequestPath: path,
		Target: cadf.Resource{
			TypeURI:     "service/resources/commitment",
			ID:          "00000000-0000-0000-0000-000000000001",
			DomainID:    "uuid-for-france",
			DomainName:  "france",
			ProjectID:   "uuid-for-paris",
			ProjectName: "paris",
			Attachments: []cadf.Attachment{must.Return(cadf.NewJSONAttachment("payload", ccr))},
		}})
	if liquidHandlesCommitments {
		assert.Equal(t, s.LiquidClients["first"].LastCommitmentChangeRequest, ccr)
		s.LiquidClients["first"].LastCommitmentChangeRequest = liquid.CommitmentChangeRequest{}
	} else {
		assert.Equal(t, s.LiquidClients["first"].LastCommitmentChangeRequest, liquid.CommitmentChangeRequest{})
	}
}

// Helper function for a failed commitment deletion.
func deleteCommitmentAndExpectError(t *testing.T, s test.Setup, tr *easypg.Tracker, uuid liquid.CommitmentUUID, ccr liquid.CommitmentChangeRequest, expect func(r httptest.Response)) {
	t.Helper()
	path := "/resources/v2/commitments/" + string(uuid)
	s.Handler.RespondTo(s.Ctx, "DELETE "+path).Expect(expect)

	// assertions
	tr.DBChanges().AssertEmpty()
	s.Auditor.ExpectEvents(t, nil...)
	assert.Equal(t, s.LiquidClients["first"].LastCommitmentChangeRequest, ccr)
}

func generateDeleteCCR(uuid liquid.CommitmentUUID, expiresAt time.Time) liquid.CommitmentChangeRequest {
	return liquid.CommitmentChangeRequest{
		AZ:          "az-one",
		InfoVersion: 1,
		ByProject: map[liquid.ProjectUUID]liquid.ProjectCommitmentChangeset{
			"uuid-for-paris": {
				ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
					"capacity": {
						TotalConfirmedBefore: 10, TotalConfirmedAfter: 0, TotalGuaranteedBefore: 0, TotalGuaranteedAfter: 0, Commitments: []liquid.Commitment{{
							UUID:      uuid,
							OldStatus: Some(liquid.CommitmentStatusConfirmed),
							NewStatus: None[liquid.CommitmentStatus](),
							Amount:    10,
							ExpiresAt: expiresAt,
						}},
					},
				},
			},
		},
	}
}

func TestCommitmentDeleteBasic(t *testing.T) {
	// run this test twice, once with commitments managed by Limes, and once managed by the liquid
	for _, manager := range []string{"limes", "liquid"} {
		t.Run("managedby="+manager, func(t *testing.T) {
			srvInfoFirst := test.DefaultLiquidServiceInfo("First")
			srvInfoSecond := test.DefaultLiquidServiceInfo("Second")
			if manager == "liquid" {
				for resName, resInfo := range srvInfoFirst.Resources {
					resInfo.HandlesCommitments = true
					srvInfoFirst.Resources[resName] = resInfo
				}
				for resName, resInfo := range srvInfoSecond.Resources {
					resInfo.HandlesCommitments = true
					srvInfoSecond.Resources[resName] = resInfo
				}
			}

			s := test.NewSetup(t,
				test.WithConfig(commitmentCreateConfigJSON),
				test.WithMockLiquidClient("first", srvInfoFirst),
				test.WithPersistedServiceInfo("first", srvInfoFirst),
				test.WithMockLiquidClient("second", srvInfoSecond),
				test.WithPersistedServiceInfo("second", srvInfoSecond),
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
			tr, tr0 := easypg.NewTracker(t, s.DB.Db)
			tr0.Ignore()

			deleteCommitmentAndExpectSuccess(t, s, tr, manager == "liquid", uuidOne, generateDeleteCCR(uuidOne, expiresAt))

			// the commitment is gone, subsequent calls fail
			deleteCommitmentAndExpectError(t, s, tr, uuidOne, liquid.CommitmentChangeRequest{}, func(r httptest.Response) {
				r.ExpectBody(t, http.StatusNotFound, []byte("no such commitment\n"))
			})
		})
	}
}

func TestCommitmentDeleteErrors(t *testing.T) {
	srvInfoFirst := test.DefaultLiquidServiceInfo("First")
	srvInfoSecond := test.DefaultLiquidServiceInfo("Second")
	for resName, resInfo := range srvInfoFirst.Resources {
		resInfo.HandlesCommitments = true
		srvInfoFirst.Resources[resName] = resInfo
	}
	for resName, resInfo := range srvInfoSecond.Resources {
		resInfo.HandlesCommitments = true
		srvInfoSecond.Resources[resName] = resInfo
	}
	s := test.NewSetup(t,
		test.WithConfig(commitmentCreateConfigJSON),
		test.WithMockLiquidClient("first", srvInfoFirst),
		test.WithPersistedServiceInfo("first", srvInfoFirst),
		test.WithMockLiquidClient("second", srvInfoSecond),
		test.WithPersistedServiceInfo("second", srvInfoSecond),
		test.WithInitialDiscovery,
		test.WithEmptyResourceRecordsAsNeeded,
	)
	tr, _ := easypg.NewTracker(t, s.DB.Db)

	// non-existing commitment
	uuidOne := liquid.CommitmentUUID("00000000-0000-0000-0000-000000000001")
	deleteCommitmentAndExpectError(t, s, tr, uuidOne, liquid.CommitmentChangeRequest{}, func(r httptest.Response) {
		r.ExpectBody(t, http.StatusNotFound, []byte("no such commitment\n"))
	})

	// create a commitment, which is 24 hours old (cannot be deleted by non-admins)
	projectParisID := s.GetProjectID("paris")
	firstCapacityID := s.GetAZResourceID("first", "capacity", "az-one")
	expiresAt := s.Clock.Now().AddDate(1, 0, 0).UTC()
	dummyCommitment := db.ProjectCommitment{
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
	}
	s.MustDBInsert(&dummyCommitment)
	tr.DBChanges().Ignore()
	s.Clock.StepBy(24 * time.Hour)

	// wrong token scope
	s.TokenValidator.Enforcer.AllowProject = false
	deleteCommitmentAndExpectError(t, s, tr, uuidOne, liquid.CommitmentChangeRequest{}, func(r httptest.Response) {
		r.ExpectBody(t, http.StatusForbidden, []byte("Forbidden\n"))
	})
	s.TokenValidator.Enforcer.AllowProject = true

	// no admin privileges
	s.TokenValidator.Enforcer.AllowCommitmentDeleteAdmin = false
	deleteCommitmentAndExpectError(t, s, tr, uuidOne, liquid.CommitmentChangeRequest{}, func(r httptest.Response) {
		r.ExpectBody(t, http.StatusForbidden, []byte("commitment cannot be deleted\n"))
	})

	// delete with admin privileges succeeds
	s.TokenValidator.Enforcer.AllowCommitmentDeleteAdmin = true
	deleteCommitmentAndExpectSuccess(t, s, tr, true, uuidOne, generateDeleteCCR(uuidOne, expiresAt))

	// another test commitment
	uuidTwo := liquid.CommitmentUUID("00000000-0000-0000-0000-000000000002")
	dummyCommitment.UUID = uuidTwo
	s.MustDBInsert(&dummyCommitment)
	tr.DBChanges().Ignore()

	// simulate unresponsive liquid
	s.LiquidClients["first"].CommitmentChangeResponse.SetError(errors.New("simulated liquid error"))
	deleteCommitmentAndExpectError(t, s, tr, uuidTwo, generateDeleteCCR(uuidTwo, expiresAt), func(r httptest.Response) {
		r.ExpectStatus(t, http.StatusInternalServerError) // the body has an error uuid which we cannot capture as of now
	})
}
