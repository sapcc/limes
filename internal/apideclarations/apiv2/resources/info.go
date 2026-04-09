// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package resourcesv2

import (
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	"github.com/sapcc/go-api-declarations/liquid"

	. "github.com/majewsky/gg/option"

	"github.com/sapcc/limes/internal/db"
)

////////////////////////////////////////////////////////////////////////////////
// /resources/v2/info API
////////////////////////////////////////////////////////////////////////////////

// InfoReport is the response type for GET /resources/v2/info.
// It contains all metadata information about the clusters services and resources.
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
	Version     int64                                  `json:"version"`
	DisplayName string                                 `json:"display_name"`
	Categories  map[liquid.CategoryName]CategoryReport `json:"categories"`
}

// CategoryReport groups resources into categories, which are defined in the config.
// It appears in ServiceInfoReport.
type CategoryReport struct {
	DisplayName string                                     `json:"display_name"`
	Resources   map[liquid.ResourceName]ResourceInfoReport `json:"resources"`
}

// ResourceInfoReport contains details about a resource.
// It appears in CategoryReport.
type ResourceInfoReport struct {
	DisplayName      string                          `json:"display_name"`
	Unit             Option[liquid.Unit]             `json:"unit,omitzero"`
	Topology         liquid.Topology                 `json:"topology"`
	HasCapacity      bool                            `json:"has_capacity"`
	HasQuota         bool                            `json:"has_quota"`
	CommitmentConfig Option[CommitmentConfiguration] `json:"commitment_config,omitzero"`
}

////////////////////////////////////////////////////////////////////////////////
// /rates/v2/info API

// V2RatesInfoReport is the response type for GET /v2/info.
// It contains all metadata information about the clusters services.
type V2RatesInfoReport struct {
	// The Area is a grouping of multiple services, which serve the same purpose.
	// E.g. compute, storage, network, etc.
	Areas map[string]V2RatesAreaReport `json:"service_areas"`
}

// V2RatesAreaReport groups services into areas, which are defined in the config.
// It appears in V2RatesInfoReport.
type V2RatesAreaReport struct {
	DisplayName string                                      `json:"display_name"`
	Services    map[db.ServiceType]V2RatesServiceInfoReport `json:"services"`
}

// V2RatesServiceInfoReport contains details about a service.
// It appears in V2RatesAreaReport.
type V2RatesServiceInfoReport struct {
	Version     int64                                     `json:"version"`
	DisplayName string                                    `json:"display_name"`
	Rates       map[liquid.RateName]V2RatesRateInfoReport `json:"rates"`
}

// V2RatesRateInfoReport contains details about a rate.
// It appears in V2RatesServiceInfoReport.
type V2RatesRateInfoReport struct {
	DisplayName string                         `json:"display_name"`
	Unit        Option[liquid.Unit]            `json:"unit,omitzero"`
	Topology    liquid.Topology                `json:"topology"`
	HasUsage    bool                           `json:"has_usage"`
	Limits      Option[V2RatesRateLimitReport] `json:"limits,omitzero"`
}

// V2RatesRateLimitReport contains details about the limits of a rate.
// Default limits might exist on cluster and project level.
// Additionally, the local limit might be set on project level.
// This object cannot exist on domain level.
type V2RatesRateLimitReport struct {
	Limit         uint64                    `json:"limit,omitempty"`
	Window        Option[limesrates.Window] `json:"window,omitzero"`
	DefaultLimit  uint64                    `json:"default_limit,omitempty"`
	DefaultWindow Option[limesrates.Window] `json:"default_window,omitzero"`
}
