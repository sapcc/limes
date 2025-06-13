// SPDX-FileCopyrightText: 2018-2020 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package limesresources

import "github.com/sapcc/go-api-declarations/limes"

// DomainReport contains aggregated data about resource usage in a domain.
// It is returned by GET requests on domains.
type DomainReport struct {
	limes.DomainInfo
	Services DomainServiceReports `json:"services"`
}

// DomainServiceReport is a substructure of DomainReport containing data for
// a single backend service.
type DomainServiceReport struct {
	limes.ServiceInfo
	Resources    DomainResourceReports  `json:"resources"`
	MaxScrapedAt *limes.UnixEncodedTime `json:"max_scraped_at,omitempty"`
	MinScrapedAt *limes.UnixEncodedTime `json:"min_scraped_at,omitempty"`
}

// DomainResourceReport is a substructure of DomainReport containing data for
// a single resource.
type DomainResourceReport struct {
	// Several fields are pointers to values to enable precise control over which fields are rendered in output.
	ResourceInfo
	QuotaDistributionModel QuotaDistributionModel   `json:"quota_distribution_model,omitempty"`
	CommitmentConfig       *CommitmentConfiguration `json:"commitment_config,omitempty"`
	// PerAZ is only rendered by Limes when the v2 API feature preview is enabled.
	PerAZ                DomainAZResourceReports `json:"per_az,omitempty"`
	DomainQuota          *uint64                 `json:"quota,omitempty"`
	ProjectsQuota        *uint64                 `json:"projects_quota,omitempty"`
	Usage                uint64                  `json:"usage"`
	PhysicalUsage        *uint64                 `json:"physical_usage,omitempty"`
	BackendQuota         *uint64                 `json:"backend_quota,omitempty"`
	InfiniteBackendQuota *bool                   `json:"infinite_backend_quota,omitempty"`
}

// DomainAZResourceReport is a substructure of DomainResourceReport containing
// quota and usage data for a single resource in an availability zone.
//
// This type is part of the v2 API feature preview.
type DomainAZResourceReport struct {
	Quota *uint64 `json:"quota,omitempty"`
	Usage uint64  `json:"usage"`
	// The keys for these maps must be commitment durations as accepted
	// by func ParseCommitmentDuration. We cannot use type CommitmentDuration
	// directly here because Go does not allow struct types as map keys.
	Committed          map[string]uint64 `json:"committed,omitempty"`
	UnusedCommitments  uint64            `json:"unused_commitments,omitempty"`
	PendingCommitments map[string]uint64 `json:"pending_commitments,omitempty"`
	PlannedCommitments map[string]uint64 `json:"planned_commitments,omitempty"`
	// UncommittedUsage can also be derived as Usage - (Committed.Values().Sum() - UnusedCommitments),
	// so this is only reported for convenience purposes.
	UncommittedUsage uint64 `json:"uncommitted_usage,omitempty"`
}

// DomainServiceReports provides fast lookup of services using a map, but serializes
// to JSON as a list.
type DomainServiceReports map[limes.ServiceType]*DomainServiceReport

// DomainResourceReports provides fast lookup of resources using a map, but serializes
// to JSON as a list.
type DomainResourceReports map[ResourceName]*DomainResourceReport

// DomainAZResourceReport is a substructure of DomainResourceReport that breaks
// down quota and usage data for a single resource by availability zone.
type DomainAZResourceReports map[limes.AvailabilityZone]*DomainAZResourceReport
