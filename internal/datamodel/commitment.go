// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package datamodel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

	. "github.com/majewsky/gg/option"
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

// DelegateChangeCommitments decides whether LiquidClient.ChangeCommitments() should be called,
// depending on the setting of liquid.ResourceInfo.HandlesCommitments. If not, it routes the
// operation to be performed locally on the database. In case the LiquidConnection is not filled,
// a LiquidClient is instantiated on the fly to perform the operation. It utilizes a given ServiceInfo so that no
// double retrieval is necessary caused by operations to assemble the liquid.CommitmentChange.
func DelegateChangeCommitments(ctx context.Context, cluster *core.Cluster, req liquid.CommitmentChangeRequest, serviceType db.ServiceType, serviceInfo liquid.ServiceInfo, dbi db.Interface) (result liquid.CommitmentChangeResponse, err error) {
	localCommitmentChanges := liquid.CommitmentChangeRequest{
		DryRun:      req.DryRun,
		AZ:          req.AZ,
		InfoVersion: req.InfoVersion,
		ByProject:   make(map[liquid.ProjectUUID]liquid.ProjectCommitmentChangeset),
	}
	remoteCommitmentChanges := liquid.CommitmentChangeRequest{
		DryRun:      req.DryRun,
		AZ:          req.AZ,
		InfoVersion: req.InfoVersion,
		ByProject:   make(map[liquid.ProjectUUID]liquid.ProjectCommitmentChangeset),
	}
	for projectUUID, projectCommitmentChangeset := range req.ByProject {
		for resourceName, resourceCommitmentChangeset := range projectCommitmentChangeset.ByResource {
			// this is just to make the tests deterministic because time.Local != local IANA time (after parsing)
			for i, commitment := range resourceCommitmentChangeset.Commitments {
				commitment.ExpiresAt = commitment.ExpiresAt.Local()
				commitment.ConfirmBy = options.Map(commitment.ConfirmBy, time.Time.Local)
				resourceCommitmentChangeset.Commitments[i] = commitment
			}

			if serviceInfo.Resources[resourceName].HandlesCommitments {
				_, exists := remoteCommitmentChanges.ByProject[projectUUID]
				if !exists {
					remoteCommitmentChanges.ByProject[projectUUID] = liquid.ProjectCommitmentChangeset{
						ByResource: make(map[liquid.ResourceName]liquid.ResourceCommitmentChangeset),
					}
				}
				remoteCommitmentChanges.ByProject[projectUUID].ByResource[resourceName] = resourceCommitmentChangeset
				continue
			}
			_, exists := localCommitmentChanges.ByProject[projectUUID]
			if !exists {
				localCommitmentChanges.ByProject[projectUUID] = liquid.ProjectCommitmentChangeset{
					ByResource: make(map[liquid.ResourceName]liquid.ResourceCommitmentChangeset),
				}
			}
			localCommitmentChanges.ByProject[projectUUID].ByResource[resourceName] = resourceCommitmentChangeset
		}
	}
	for projectUUID, projectCommitmentChangeset := range localCommitmentChanges.ByProject {
		if serviceInfo.CommitmentHandlingNeedsProjectMetadata {
			pcs := projectCommitmentChangeset
			pcs.ProjectMetadata = req.ByProject[projectUUID].ProjectMetadata
			localCommitmentChanges.ByProject[projectUUID] = pcs
		}
	}
	for projectUUID, remoteCommitmentChangeset := range remoteCommitmentChanges.ByProject {
		if serviceInfo.CommitmentHandlingNeedsProjectMetadata {
			rcs := remoteCommitmentChangeset
			rcs.ProjectMetadata = req.ByProject[projectUUID].ProjectMetadata
			remoteCommitmentChanges.ByProject[projectUUID] = rcs
		}
	}

	// check remote
	if len(remoteCommitmentChanges.ByProject) != 0 {
		var liquidClient core.LiquidClient
		if len(cluster.LiquidConnections) == 0 {
			// find the right ServiceType
			liquidClient, err = cluster.LiquidClientFactory(serviceType)
			if err != nil {
				return result, err
			}
		} else {
			liquidClient = cluster.LiquidConnections[serviceType].LiquidClient
		}
		commitmentChangeResponse, err := liquidClient.ChangeCommitments(ctx, remoteCommitmentChanges)
		if err != nil {
			return result, fmt.Errorf("failed to retrieve liquid ChangeCommitment response for service %s: %w", serviceType, err)
		}
		if commitmentChangeResponse.RejectionReason != "" {
			return commitmentChangeResponse, nil
		}
	}

	// check local
	if len(localCommitmentChanges.ByProject) != 0 {
		canAcceptLocally, err := CanAcceptCommitmentChangeRequest(localCommitmentChanges, serviceType, cluster, dbi)
		if err != nil {
			return result, fmt.Errorf("failed to check local ChangeCommitment: %w", err)
		}
		if !canAcceptLocally {
			return liquid.CommitmentChangeResponse{
				RejectionReason: "not enough capacity!",
				RetryAt:         None[time.Time](),
			}, nil
		}
	}

	return result, nil
}

// LiquidProjectMetadataFromDBProject converts a db.Project into liquid.ProjectMetadata
// only if the given serviceInfo requires it for commitment handling.
func LiquidProjectMetadataFromDBProject(dbProject db.Project, domain db.Domain, serviceInfo liquid.ServiceInfo) Option[liquid.ProjectMetadata] {
	if !serviceInfo.CommitmentHandlingNeedsProjectMetadata {
		return None[liquid.ProjectMetadata]()
	}
	return Some(core.KeystoneProjectFromDB(dbProject, core.KeystoneDomain{UUID: domain.UUID, Name: domain.Name}).ForLiquid())
}
