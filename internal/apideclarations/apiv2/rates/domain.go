// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package ratesv2

import (
	"github.com/sapcc/go-api-declarations/liquid"

	"github.com/sapcc/limes/internal/apideclarations/apiv2/common"
	"github.com/sapcc/limes/internal/db"

	. "go.xyrillian.de/gg/option"
)

// DomainGetResponse is the response type for GET /rates/v2/domains(/:domain_uuid)?.
// It contains the latest state of all dynamic rate data for one or many domains.
// This can only contain summed data from project level.
type DomainGetResponse struct {
	// InfoReport is what GET /rates/v2/info would report.
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

// DomainCategoryReport groups rates into categories, which are defined in the config.
// It appears in [DomainServiceReport].
type DomainCategoryReport struct {
	Rates map[liquid.RateName]DomainRateReport `json:"rates"`
}

// DomainRateReport contains data for one rate.
// It appears in [DomainCategoryReport].
type DomainRateReport struct {
	// UsageAsBigint is a big.Int that has to be serialized as string.
	// If the rate does not report usage, the rate is omitted from the DomainCategoryReport.Rates map.
	UsageAsBigint string `json:"usage_as_bigint"`
}
