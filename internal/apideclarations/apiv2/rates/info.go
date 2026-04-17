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
	Version     int64                              `json:"version"`
	DisplayName string                             `json:"display_name"`
	Rates       map[liquid.RateName]RateInfoReport `json:"rates"`
}

// RateInfoReport contains details about a rate.
// it appears in [ServiceInfoReport]
type RateInfoReport struct {
	DisplayName          string                    `json:"display_name"`
	Unit                 liquid.Unit               `json:"unit,omitzero"`
	Topology             liquid.Topology           `json:"topology"`
	HasUsage             bool                      `json:"has_usage"`
	ProjectDefaultLimit  uint64                    `json:"project_default_limit,omitempty"`
	ProjectDefaultWindow Option[limesrates.Window] `json:"project_default_window,omitzero"`
}
