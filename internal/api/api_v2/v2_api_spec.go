// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

// Package api_v2 provides the specification for the [Limes] /v2 API.
//
// # Concepts
// TODO: copy this section from v1 API when the API is finished
//
// # Common Request Headers
// **X-Auth-Token**: As with all OpenStack services, this header must always contain a [Keystone token](https://docs.openstack.org/api-ref/identity/v3/index.html#password-authentication-with-scoped-authorization).
// In the /v2 API, the token often determines the scope of the data you get back from the API.
// I.e. when you provide a project-scoped token, you get back data for that project.
// When you provide a domain-scoped token, you get back data for that domain.
// When you provide a cloud-admin-scoped token, you get back all data.
// Unscoped tokens are not supported.
//
// # Common Query Arguments
// TODO: fill when implemented
//
// # Endpoints
// ## GET /resources/v2/info
// Returns information about the clusters resources.
// **On success**: Returns an object of type V2ResourcesInfoReport.
// **On failure**: Returns an error string with an appropriate HTTP status code.
//
// ## GET /resources/v2/cluster
// TODO: fill when implemented
//
// ## GET /resources/v2/domains(/:domain_id)?
// TODO: fill when implemented
//
// ## GET /resources/v2/projects(/:project_id)?
// TODO: fill when implemented
//
// ## GET /resources/v2/availability
// TODO: fill when implemented
//
// ## GET /rates/v2/info
// Returns information about the clusters rates.
// **On success**: Returns an object of type V2RatesInfoReport.
// **On failure**: Returns an error string with an appropriate HTTP status code.
//
// ## GET /rates/v2/cluster
// TODO: fill when implemented
//
// ## GET /rates/v2/domains(/:domain_id)?
// TODO: fill when implemented
//
// ## GET /rates/v2/projects(/:project_id)?
// TODO: fill when implemented
//
// [Limes]: https://github.com/sapcc/limes
package api_v2

import (
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"

	. "github.com/majewsky/gg/option"

	"github.com/sapcc/limes/internal/db"
)

////////////////////////////////////////////////////////////////////////////////
// /resources/v2/info API

// V2ResourcesInfoReport is the response type for GET /v2/info.
// It contains all metadata information about the clusters services.
type V2ResourcesInfoReport struct {
	// The Area is a grouping of multiple services, which serve the same purpose.
	// E.g. compute, storage, network, etc.
	Areas map[string]V2ResourcesAreaReport `json:"service_areas"`
}

// V2ResourcesAreaReport groups services into areas, which are defined in the config.
// It appears in V2ResourcesInfoReport.
type V2ResourcesAreaReport struct {
	DisplayName string                                          `json:"display_name"`
	Services    map[db.ServiceType]V2ResourcesServiceInfoReport `json:"services"`
}

// V2ResourcesServiceInfoReport contains details about a service.
// It appears in V2ResourcesAreaReport.
type V2ResourcesServiceInfoReport struct {
	Version     int64                                             `json:"version"`
	DisplayName string                                            `json:"display_name"`
	Categories  map[liquid.CategoryName]V2ResourcesCategoryReport `json:"categories"`
}

// V2ResourcesCategoryReport groups resources into categories, which are defined in the config.
// It appears in V2ResourcesServiceInfoReport.
type V2ResourcesCategoryReport struct {
	DisplayName string                                                `json:"display_name"`
	Resources   map[liquid.ResourceName]V2ResourcesResourceInfoReport `json:"resources"`
}

// V2ResourcesResourceInfoReport contains details about a resource.
// It appears in V2ResourcesCategoryReport.
type V2ResourcesResourceInfoReport struct {
	DisplayName      string                                         `json:"display_name"`
	Unit             Option[liquid.Unit]                            `json:"unit,omitzero"`
	Topology         liquid.Topology                                `json:"topology"`
	HasCapacity      bool                                           `json:"has_capacity"`
	HasQuota         bool                                           `json:"has_quota"`
	CommitmentConfig Option[limesresources.CommitmentConfiguration] `json:"commitment_config,omitzero"`
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
