// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"github.com/sapcc/go-api-declarations/liquid"
	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/limes/internal/db"
)

// GenericReportOpts contains query parameter options shared between
// resources and rates reports.
// It appears in types ResourceReportOpts and RateReportOpts.
type GenericReportOpts struct {
	// Area is a grouping, used to filter for multiple services
	Area Option[string] `q:"area"`
	// ServiceType filters services by type
	ServiceType Option[db.ServiceType] `q:"service"`
	// Category is a grouping, used to filter for multiple resources or rates
	Category Option[liquid.CategoryName] `q:"category"`
	// enrich response with InfoReport
	WithInfo bool `q:"with,value:info"`
}

// ResourceReportOpts contains query parameter options for resource reports.
// It appears in types ClusterResourceReportOpts, DomainResourceReportOpts and ProjectResourceReportOpts.
type ResourceReportOpts struct {
	GenericReportOpts
	// ResourceName filters resources by name
	ResourceName Option[liquid.ResourceName] `q:"resource"`
	// WithCommitmentStats enriches the response with Committed values
	WithCommitmentStats bool `q:"with,value:commitment_stats"`
}

// ClusterResourceReportOpts contains query parameter options for cluster
// resource reports.
type ClusterResourceReportOpts struct {
	ResourceReportOpts
	// WithTiming enriches the response with ScrapedAt values
	WithTiming bool `q:"with,value:timing"`
	// WithSubcapacities enriches the response with Subcapacities values
	WithSubcapacities bool `q:"with,value:subcapacities"`
}

// DomainResourceReportOpts contains query parameter options for domain
// resource reports.
type DomainResourceReportOpts struct {
	ResourceReportOpts
}

// ProjectResourceReportOpts contains query parameter options for project
// resource reports.
type ProjectResourceReportOpts struct {
	ResourceReportOpts
	// WithUserSpecifiedConstraints enriches the response with MaxQuota and ForbidAutogrowth values
	WithUserSpecifiedConstraints bool `q:"with,value:constraints"`
	// WithTiming enriches the response with ScrapedAt values which is only allowed for users with certain permissions
	WithTiming bool `q:"with,value:timing"`
	// WithSubresources enriches the response with Subresources values which is only allowed for users with certain permissions
	WithSubresources bool `q:"with,value:subresources"`
	// WithHistoricalUsage enriches the response with HistoricalUsage values which is only allowed for users with certain permissions
	WithHistoricalUsage bool `q:"with,value:historical_usage"`
	// DomainUUID is a special entity filter which is only allowed for users with certain permissions
	DomainUUID Option[string] `q:"domain_uuid"`
}

// RateReportOpts contains query parameter options for rate reports.
// It appears in type ClusterRateReportOpts, DomainRateReportOpts and ProjectRateReportOpts.
type RateReportOpts struct {
	GenericReportOpts
	// RateName filters rates by name
	RateName Option[liquid.RateName] `q:"rate"`
}

// ClusterRateReportOpts contains query parameter options for cluster
// rate reports.
type ClusterRateReportOpts struct {
	RateReportOpts
}

// DomainRateReportOpts contains query parameter options for domain
// rate reports.
type DomainRateReportOpts struct {
	RateReportOpts
}

// ProjectRateReportOpts contains query parameter options for project
// rate reports.
type ProjectRateReportOpts struct {
	RateReportOpts
	// WithTiming enriches the response with ScrapedAt values which is only allowed for users with certain permissions
	WithTiming bool `q:"with,value:timing"`
	// DomainUUID is a special entity filter which is only allowed for users with certain permissions
	DomainUUID Option[string] `q:"domain_uuid"`
}
