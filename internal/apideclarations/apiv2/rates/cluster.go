// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package ratesv2

import (
	"github.com/sapcc/go-api-declarations/liquid"
	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/limes/internal/db"
)

// ClusterGetResponse is the response type for GET /rates/v2/cluster.
// It contains the latest state of all dynamic rate data for the whole cluster.
// This can only contain summed data from project level.
type ClusterGetResponse struct {
	// InfoReport is what GET /rates/v2/info would report.
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
	Categories map[liquid.CategoryName]ClusterCategoryReport `json:"categories"`
}

// ClusterCategoryReport groups rates into categories, which are defined in the config.
// It appears in [ClusterServiceReport].
type ClusterCategoryReport struct {
	Rates map[liquid.RateName]ClusterRateReport `json:"rates"`
}

// ClusterRateReport contains data for one rate.
// It appears in [ClusterCategoryReport].
type ClusterRateReport struct {
	// UsageAsBigint is a big.Int that has to be serialized as string.
	UsageAsBigint string `json:"usage_as_bigint"`
}
