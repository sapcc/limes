// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package ratesv2

import (
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	"github.com/sapcc/go-api-declarations/liquid"

	. "github.com/majewsky/gg/option"

	"github.com/sapcc/limes/internal/db"
)

// InfoReport is the response type for GET /rates/v2/info.
// It contains all metadata information about the clusters services and rates.
type InfoReport struct {
	// The Area is a grouping of multiple services, which serve the same purpose.
	// E.g. compute, storage, network, etc.
	Areas map[string]AreaReport `json:"service_areas"`
}

// AreaReport groups services into areas, which are defined in the config.
// It appears in InfoReport.
type AreaReport struct {
	DisplayName string                               `json:"display_name"`
	Services    map[db.ServiceType]ServiceInfoReport `json:"services"`
}

// ServiceInfoReport contains details about a service.
// It appears in AreaReport.
type ServiceInfoReport struct {
	Version     int64                              `json:"version"`
	DisplayName string                             `json:"display_name"`
	Rates       map[liquid.RateName]RateInfoReport `json:"rates"`
}

// RateInfoReport contains details about a rate.
// It appears in ServiceInfoReport.
type RateInfoReport struct {
	DisplayName string                  `json:"display_name"`
	Unit        Option[liquid.Unit]     `json:"unit,omitzero"`
	Topology    liquid.Topology         `json:"topology"`
	HasUsage    bool                    `json:"has_usage"`
	Limits      Option[RateLimitReport] `json:"limits,omitzero"`
}

// RateLimitReport contains details about the limits of a rate.
// Default limits might exist on cluster and project level.
// Additionally, the local limit might be set on project level.
// This object cannot exist on domain level.
type RateLimitReport struct {
	Limit         uint64                    `json:"limit,omitempty"`
	Window        Option[limesrates.Window] `json:"window,omitzero"`
	DefaultLimit  uint64                    `json:"default_limit,omitempty"`
	DefaultWindow Option[limesrates.Window] `json:"default_window,omitzero"`
}
