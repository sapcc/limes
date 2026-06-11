// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package resourcesv2

import (
	"encoding/json"

	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/limes/internal/db"
)

// ClusterGetResponse is the response type for GET /resources/v2/cluster.
// It contains the latest state of all dynamic resource data for the whole cluster.
// This can contain summed data from project level or data only available on cluster level.
type ClusterGetResponse struct {
	// InfoReport is what GET /resources/v2/info would report.
	// It is only returned when the respective query option with=info is set.
	InfoReport    Option[InfoReport] `json:"service_info,omitzero"`
	ClusterReport ClusterReport      `json:"cluster_report"`
}

// ClusterReport groups services of the whole cluster into areas,
// which are defined in the config.
// It appears in [ClusterGetResponse].
type ClusterReport struct {
	Areas map[string]ClusterAreaReport `json:"service_areas"`
}

// ClusterAreaReport contains data for one area.
// It appears in [ClusterReport].
type ClusterAreaReport struct {
	Services map[db.ServiceType]ClusterServiceReport `json:"services"`
}

// ClusterServiceReport contains data for one service.
// It appears in [ClusterAreaReport].
type ClusterServiceReport struct {
	// ScrapedAt expresses when this service was last successfully queried from the backend.
	// It is only returned when the respective query option with=timing is set.
	ScrapedAt  Option[limes.UnixEncodedTime]                 `json:"scraped_at,omitzero"`
	Categories map[liquid.CategoryName]ClusterCategoryReport `json:"categories"`
}

// ClusterCategoryReport groups resources into categories, which are defined in the config.
// It appears in [ClusterServiceReport].
type ClusterCategoryReport struct {
	Resources map[liquid.ResourceName]ClusterResourceReport `json:"resources"`
}

// ClusterResourceReport contains data for one resource.
// It appears in [ClusterCategoryReport].
type ClusterResourceReport struct {
	// TODO: note which AZs can surface here, depending on topology and various scenarios.
	AvailabilityZones map[limes.AvailabilityZone]ClusterAvailabilityZoneReport `json:"availability_zones"`
}

// ClusterAvailabilityZoneReport contains the data for an availability zone.
// It appears in [ClusterResourceReport].
type ClusterAvailabilityZoneReport struct {
	// Capacity is what Limes considers the usable amount of this resource.
	// It is calculated as RawCapacity*OvercommitFactor, where OvercommitFactor=1 when not configured.
	Capacity uint64 `json:"capacity"`
	// RawCapacity is what the service reports as capacity for this resource.
	RawCapacity uint64 `json:"raw_capacity"`
	// OverallUsage is what the service reports as overall usage, including usages that cannot be attributed to
	// OpenStack-projects (like management workload). This is only shown if the backend does report this figure.
	OverallUsage Option[uint64] `json:"overall_usage,omitzero"`
	// Usage is the sum of the usages across all projects as reported by the service.
	Usage uint64 `json:"usage"`
	// CommittedConfirmed is the sum of committed and confirmed amounts across projects grouped by their CommitmentDuration.
	// It is only returned when the respective query option with=commitment_stats is set.
	CommittedConfirmed map[limesresources.CommitmentDuration]uint64 `json:"committed_confirmed,omitempty"`
	// CommittedConfirmedUnutilized is the sum of CommittedConfirmed - Usage for each project in this cluster.
	// If computed as sum of CommittedConfirmed - sum of Usage, the result is semantically wrong (commitments cannot cover other projects).
	// It is only returned when the respective query option with=commitment_stats is set.
	CommittedConfirmedUnutilized Option[uint64] `json:"committed_confirmed_unutilized,omitzero"`
	// UncommittedUsage can also be derived as sum of Usage - (Committed.Values().Sum() - CommittedConfirmedUnutilized).
	// so this is only reported for convenience purposes.
	// It is only returned when the respective query option with=commitment_stats is set.
	UncommittedUsage Option[uint64] `json:"uncommitted_usage,omitzero"`
	// PhysicalUsage is collected per project and then summed, same as Usage.
	PhysicalUsage Option[uint64] `json:"physical_usage,omitzero"`
	// Subcapacities is formatted as json.RawMessage for convenience for reading from the database.
	// The content will be a marshalled []liquid.Subcapacity.
	// It is only returned when the respective query option with=subcapacities is set.
	Subcapacities json.RawMessage `json:"subcapacities,omitempty"`
}
