// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package resourcesv2

import (
	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"

	"github.com/sapcc/limes/internal/apideclarations/apiv2/common"
	"github.com/sapcc/limes/internal/db"

	. "go.xyrillian.de/gg/option"
)

// DomainGetResponse is the response type for GET /resources/v2/domains(/:domain_uuid)?.
// It contains the latest state of all dynamic resource data for one or many domains.
// This can only contain summed data from project level.
type DomainGetResponse struct {
	// InfoReport is what GET /resources/v2/info would report.
	// It is only returned when the respective query option with=info is set.
	InfoReport    Option[InfoReport]      `json:"info,omitzero"`
	DomainReports map[string]DomainReport `json:"domains"`
}

// DomainReport groups all services for one domain into areas,
// which are defined in the config.
// It appears in [DomainGetResponse].
type DomainReport struct {
	common.DomainMetadata
	Areas map[string]DomainAreaReport `json:"service_areas"`
}

// DomainAreaReport contains data for one area.
// It appears in [DomainReport].
type DomainAreaReport struct {
	Services map[db.ServiceType]DomainServiceReport `json:"services"`
}

// DomainServiceReport contains data for one service.
// It appears in [DomainAreaReport].
type DomainServiceReport struct {
	Categories map[liquid.CategoryName]DomainCategoryReport `json:"categories"`
}

// DomainCategoryReport groups resources into categories, which are defined in the config.
// It appears in [DomainServiceReport].
type DomainCategoryReport struct {
	Resources map[liquid.ResourceName]DomainResourceReport `json:"resources"`
}

// DomainResourceReport contains data for one resource.
// It appears in [DomainCategoryReport].
type DomainResourceReport struct {
	// TODO: note which AZs can surface here, depending on topology and various scenarios.
	AvailabilityZones map[limes.AvailabilityZone]DomainAvailabilityZoneReport `json:"availability_zones"`
}

// DomainAvailabilityZoneReport contains the data for an availability zone.
// It appears in [DomainResourceReport].
type DomainAvailabilityZoneReport struct {
	// Usage is the sum of the usages across projects in this domain as reported by the service.
	Usage uint64 `json:"usage"`
	// Committed is the sum of committed amounts across projects in this domain grouped by their Status and then CommitmentDuration.
	// It is only returned when the respective query option with=commitment_stats is set.
	Committed map[liquid.CommitmentStatus]map[limesresources.CommitmentDuration]uint64 `json:"committed,omitempty"`
	// CommittedConfirmedUnutilized is the sum of CommittedConfirmed - Usage for each project in this domain.
	// If computed as sum of CommittedConfirmed - sum of Usage, the result is semantically wrong (commitments cannot cover other projects).
	// It is only returned when the respective query option with=commitment_stats is set.
	CommittedConfirmedUnutilized Option[uint64] `json:"committed_confirmed_unutilized,omitzero"`
	// UncommittedUsage can also be derived as sum of Usage - (Committed.Values().Sum() - CommittedConfirmedUnutilized).
	// so this is only reported for convenience purposes.
	// It is only returned when the respective query option with=commitment_stats is set.
	UncommittedUsage Option[uint64] `json:"uncommitted_usage,omitzero"`
	// PhysicalUsage is collected per project and then summed, same as Usage.
	PhysicalUsage Option[uint64] `json:"physical_usage,omitzero"`
}
