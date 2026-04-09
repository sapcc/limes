// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package resourcesv2

import (
	"github.com/majewsky/gg/option"
	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
)

// CommitmentConfiguration describes how commitments are configured for a given resource.
//
// This appears as a field on resource reports, if the respective resource allows commitments.
type CommitmentConfiguration struct {
	// Allowed durations for commitments on this resource.
	Durations []limesresources.CommitmentDuration `json:"durations"`
	// If shown, commitments must be created with `confirm_by` at or after this timestamp.
	MinConfirmBy option.Option[limes.UnixEncodedTime] `json:"min_confirm_by,omitzero"`
}
