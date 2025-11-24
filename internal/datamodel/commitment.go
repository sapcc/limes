// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package datamodel

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/majewsky/gg/options"
	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/must"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"
)

// GenerateTransferToken generates a token that is used to transfer a commitment from a source to a target project.
// The token will be attached to the commitment that will be transferred and stored in the database until the transfer is concluded.
func GenerateTransferToken() string {
	tokenBytes := make([]byte, 24)
	_, err := rand.Read(tokenBytes)
	if err != nil {
		panic(err.Error())
	}
	return hex.EncodeToString(tokenBytes)
}

// GenerateProjectCommitmentUUID generates a random ProjectCommitmentUUID.
func GenerateProjectCommitmentUUID() liquid.CommitmentUUID {
	// UUID generation will only raise an error if reading from /dev/urandom fails,
	// which is a wildly unexpected OS-level error and thus fine as a fatal error
	return liquid.CommitmentUUID(must.Return(uuid.NewV4()).String())
}

// BuildSplitCommitment prepares a new commitment instance whose creation context
// indicates that it was split from the given existing commitment. It is used in
// the implementation of various API endpoints that can implicitly split commitments
// if necessary.
func BuildSplitCommitment(dbCommitment db.ProjectCommitment, amount uint64, now time.Time, generateProjectCommitmentUUID func() liquid.CommitmentUUID) (db.ProjectCommitment, error) {
	creationContext := db.CommitmentWorkflowContext{
		Reason:                 db.CommitmentReasonSplit,
		RelatedCommitmentIDs:   []db.ProjectCommitmentID{dbCommitment.ID},
		RelatedCommitmentUUIDs: []liquid.CommitmentUUID{dbCommitment.UUID},
	}
	buf, err := json.Marshal(creationContext)
	if err != nil {
		return db.ProjectCommitment{}, err
	}
	return db.ProjectCommitment{
		UUID:                generateProjectCommitmentUUID(),
		ProjectID:           dbCommitment.ProjectID,
		AZResourceID:        dbCommitment.AZResourceID,
		Amount:              amount,
		Duration:            dbCommitment.Duration,
		CreatedAt:           now,
		CreatorUUID:         dbCommitment.CreatorUUID,
		CreatorName:         dbCommitment.CreatorName,
		ConfirmBy:           dbCommitment.ConfirmBy,
		ConfirmedAt:         dbCommitment.ConfirmedAt,
		ExpiresAt:           dbCommitment.ExpiresAt,
		CreationContextJSON: json.RawMessage(buf),
		Status:              dbCommitment.Status,
	}, nil
}

// CanDeleteCommitment checks whether a user with a certain token can delete a commitment at the current time.
// This is either a regular user who deletes the commitment within 24 hours of creation or an admin.
func CanDeleteCommitment(token *gopherpolicy.Token, commitment db.ProjectCommitment, timeNow func() time.Time) bool {
	// up to 24 hours after creation of fresh commitments, future commitments can still be deleted by their creators
	if commitment.Status == liquid.CommitmentStatusPlanned || commitment.Status == liquid.CommitmentStatusPending || commitment.Status == liquid.CommitmentStatusConfirmed {
		var creationContext db.CommitmentWorkflowContext
		err := json.Unmarshal(commitment.CreationContextJSON, &creationContext)
		if err == nil && creationContext.Reason == db.CommitmentReasonCreate && timeNow().Before(commitment.CreatedAt.Add(24*time.Hour)) {
			if token.Check("project:edit") {
				return true
			}
		}
	}

	// afterwards, a more specific permission is required to delete it
	//
	// This protects cloud admins making capacity planning decisions based on future commitments
	// from having their forecasts ruined by project admins suffering from buyer's remorse.
	return token.Check("project:uncommit")
}

// ConvertCommitmentToDisplayForm transforms a db.ProjectCommitment into a limesresources.Commitment for displaying
// to the user on the API or usage within the audit log.
func ConvertCommitmentToDisplayForm(c db.ProjectCommitment, loc core.AZResourceLocation, apiIdentity core.ResourceRef, canBeDeleted bool, unit limes.Unit) limesresources.Commitment {
	return limesresources.Commitment{
		ID:               int64(c.ID),
		UUID:             string(c.UUID),
		ServiceType:      apiIdentity.ServiceType,
		ResourceName:     apiIdentity.Name,
		AvailabilityZone: loc.AvailabilityZone,
		Amount:           c.Amount,
		Unit:             unit,
		Duration:         c.Duration,
		CreatedAt:        limes.UnixEncodedTime{Time: c.CreatedAt},
		CreatorUUID:      c.CreatorUUID,
		CreatorName:      c.CreatorName,
		CanBeDeleted:     canBeDeleted,
		ConfirmBy:        options.Map(c.ConfirmBy, util.IntoUnixEncodedTime).AsPointer(),
		ConfirmedAt:      options.Map(c.ConfirmedAt, util.IntoUnixEncodedTime).AsPointer(),
		ExpiresAt:        limes.UnixEncodedTime{Time: c.ExpiresAt},
		TransferStatus:   c.TransferStatus,
		TransferToken:    c.TransferToken.AsPointer(),
		Status:           c.Status,
		NotifyOnConfirm:  c.NotifyOnConfirm,
		WasRenewed:       c.RenewContextJSON.IsSome(),
	}
}
