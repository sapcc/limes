// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package limesrates

import (
	"github.com/sapcc/go-api-declarations/limes"
)

// ClusterReport contains aggregated data about resource usage in a cluster.
// It is returned by GET endpoints for clusters.
type ClusterReport struct {
	limes.ClusterInfo
	Services ClusterServiceReports `json:"services"`
}

// ClusterServiceReport is a substructure of ClusterReport containing data for
// a single backend service.
type ClusterServiceReport struct {
	limes.ServiceInfo
	Rates        ClusterRateReports     `json:"rates,omitempty"`
	MaxScrapedAt *limes.UnixEncodedTime `json:"max_scraped_at,omitempty"`
	MinScrapedAt *limes.UnixEncodedTime `json:"min_scraped_at,omitempty"`
}

// ClusterRateReport is a substructure of ClusterServiceReport containing data
// for a single rate.
type ClusterRateReport struct {
	RateInfo
	Limit  uint64 `json:"limit,omitempty"`
	Window Window `json:"window,omitempty"`
}

// ClusterServiceReports provides fast lookup of services by service type, but
// serializes to JSON as a list.
type ClusterServiceReports map[limes.ServiceType]*ClusterServiceReport

// ClusterRateReports provides fast lookup of rates using a map, but
// serializes to JSON as a list.
type ClusterRateReports map[RateName]*ClusterRateReport
