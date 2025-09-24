// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package datamodel

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/must"

	"github.com/sapcc/limes/internal/db"
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
