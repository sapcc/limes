// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-bits/must"

	"github.com/sapcc/limes/internal/db"
)

func intoUnixEncodedTime(t time.Time) limes.UnixEncodedTime {
	return limes.UnixEncodedTime{Time: t}
}

func fromUnixEncodedTime(t limes.UnixEncodedTime) time.Time {
	return t.Time
}

// Generates a token that is used to transfer a commitment from a source to a target project.
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
func GenerateProjectCommitmentUUID() db.ProjectCommitmentUUID {
	// UUID generation will only raise an error if reading from /dev/urandom fails,
	// which is a wildly unexpected OS-level error and thus fine as a fatal error
	return db.ProjectCommitmentUUID(must.Return(uuid.NewV4()).String())
}
