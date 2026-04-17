// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package resourcesv2

import (
	"github.com/sapcc/go-api-declarations/liquid"

	. "github.com/majewsky/gg/option"

	"github.com/sapcc/limes/internal/db"
)

// InfoReport is the response type for GET /resources/v2/info.
// It contains all metadata information about the clusters services and resources.
type InfoReport struct {
	// The Area is a grouping of multiple services, which serve the same purpose.
	// E.g. compute, storage, network, etc.
	Areas map[string]AreaInfoReport `json:"service_areas"`
}

// AreaInfoReport groups services into areas, which are defined in the config.
// it appears in [InfoReport]
type AreaInfoReport struct {
	DisplayName string                               `json:"display_name"`
	Services    map[db.ServiceType]ServiceInfoReport `json:"services"`
}

// ServiceInfoReport contains details about a service.
// it appears in [AreaInfoReport]
type ServiceInfoReport struct {
	Version     int64                                      `json:"version"`
	DisplayName string                                     `json:"display_name"`
	Categories  map[liquid.CategoryName]CategoryInfoReport `json:"categories"`
}

// CategoryInfoReport groups resources into categories, which are defined in the config.
// it appears in [ServiceInfoReport]
type CategoryInfoReport struct {
	DisplayName string                                     `json:"display_name"`
	Resources   map[liquid.ResourceName]ResourceInfoReport `json:"resources"`
}

// ResourceInfoReport contains details about a resource.
// it appears in [CategoryInfoReport]
type ResourceInfoReport struct {
	DisplayName      string                          `json:"display_name"`
	Unit             liquid.Unit                     `json:"unit,omitzero"`
	Topology         liquid.Topology                 `json:"topology"`
	HasCapacity      bool                            `json:"has_capacity"`
	HasQuota         bool                            `json:"has_quota"`
	CommitmentConfig Option[CommitmentConfiguration] `json:"commitment_config,omitzero"`
}
