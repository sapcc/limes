// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package resourcesv2

import (
	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/limes/internal/db"
)

// CommitmentList is the response payload format for GET /v2/commitments and POST /v2/commitments/:uuid/split.
type CommitmentList struct {
	Commitments []Commitment `json:"commitments"`
}

// Commitment is the response payload format for GET /resources/v2/commitments/:uuid and several endpoints that create or modify commitments.
type Commitment struct {
	UUID liquid.CommitmentUUID `json:"uuid"`
	// Amount refers to the amount of resource that is committed, in terms of the unit of the committed resource.
	Amount   uint64                            `json:"amount"`
	Duration limesresources.CommitmentDuration `json:"duration"`

	ProjectUUID      liquid.ProjectUUID     `json:"project_id"`
	ServiceType      db.ServiceType         `json:"service_type"`
	ResourceName     liquid.ResourceName    `json:"resource_name"`
	AvailabilityZone limes.AvailabilityZone `json:"availability_zone"`

	Status liquid.CommitmentStatus `json:"status"`
	// TransferStatus and TransferToken are only shown while the commitment is marked for transfer.
	TransferStatus limesresources.CommitmentTransferStatus `json:"transfer_status,omitempty"`
	TransferToken  Option[string]                          `json:"transfer_token,omitzero"`

	CreatedAt limes.UnixEncodedTime `json:"created_at"`
	// CreatorUUID and CreatorName identify the user who created this commitment.
	// CreatorName is in the format `fmt.Sprintf("%s@%s", userName, userDomainName)`and intended for informational displays only.
	CreatorUUID string `json:"creator_uuid,omitempty"`
	CreatorName string `json:"creator_name,omitempty"`
	// CanBeDeleted will be true if the commitment can be deleted by the same user who saw this object in response to a GET query.
	CanBeDeleted bool `json:"can_be_deleted,omitempty"`
	// ConfirmBy is unset if and only if the commitment was created in status "confirmed".
	ConfirmBy Option[limes.UnixEncodedTime] `json:"confirm_by,omitzero"`
	// ConfirmedAt is only filled after the commitment was confirmed.
	ConfirmedAt Option[limes.UnixEncodedTime] `json:"confirmed_at,omitzero"`
	ExpiresAt   limes.UnixEncodedTime         `json:"expires_at"`
	// UpdatedAt is updated at least every time any field of this type changes.
	UpdatedAt limes.UnixEncodedTime `json:"updated_at"`

	// NotifyOnConfirm can only be set if ConfirmBy is filled.
	// If true, a mail notification will be set to the project owners when the commitment is confirmed.
	NotifyOnConfirm bool `json:"notify_on_confirm,omitempty"`
	// WasRenewed indicates whether this commitment has been renewed.
	// This means that a new commitment was created that will be confirmed when this commitment is set to expire.
	WasRenewed bool `json:"was_renewed,omitempty"`
}

// CommitmentRequest is the request payload format for POST /resources/v2/commitments/new.
//
// See documentation on [Commitment] for the semantics of all fields.
// Documentation on this type's fields only mentions specifics related to the commitment creation process.
type CommitmentRequest struct {
	// DryRun can be set to true to avoid any side effects: No data will be saved in the system.
	// The response from a dry run will be identical to if a commitment had actually been created,
	// except that the UUID will be set to a dummy value.
	DryRun bool   `json:"dry_run"`
	Amount uint64 `json:"amount"`
	// Duration must be one of the values that appear in the resource's [ResourceInfoReport].
	Duration limesresources.CommitmentDuration `json:"duration"`

	ProjectUUID      liquid.ProjectUUID     `json:"project_id"`
	ServiceType      db.ServiceType         `json:"service_type"`
	ResourceName     liquid.ResourceName    `json:"resource_name"`
	AvailabilityZone limes.AvailabilityZone `json:"availability_zone"`

	// Status must be one of:
	//   - liquid.CommitmentStatusPlanned
	//   - liquid.CommitmentStatusPending
	//   - liquid.CommitmentStatusConfirmed
	//   - liquid.CommitmentStatusGuaranteed (TODO: coming soon)
	Status liquid.CommitmentStatus `json:"status"`
	// ConfirmBy must be set for statuses "planned" and "guaranteed", and may not be set otherwise.
	// Commitments created in status "pending" will have a ConfirmBy value equal to the current time.
	ConfirmBy Option[limes.UnixEncodedTime] `json:"confirm_by,omitzero"`
	// NotifyOnConfirm may not be set for commitments that are created in status "confirmed".
	NotifyOnConfirm bool `json:"notify_on_confirm,omitempty"`
}
