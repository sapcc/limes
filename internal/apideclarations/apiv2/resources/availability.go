// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package resourcesv2

import (
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"

	"github.com/sapcc/limes/internal/db"
)

// AvailabilityReport is the response type for GET /v2/resources/availability.
type AvailabilityReport struct {
	Areas map[string]AreaAvailabilityReport `json:"service_areas"`
}

// AreaAvailabilityReport groups services into areas, which are defined in the Limes config.
// It appears in [AvailabilityReport].
type AreaAvailabilityReport struct {
	Services map[db.ServiceType]ServiceAvailabilityReport `json:"services"`
}

// ServiceAvailabilityReport contains details about a service.
// It appears in [AreaAvailabilityReport].
type ServiceAvailabilityReport struct {
	Categories map[liquid.CategoryName]CategoryAvailabilityReport `json:"categories"`
}

// CategoryAvailabilityReport groups resources into categories, which are defined in the Limes config.
// It appears in [ServiceAvailabilityReport].
type CategoryAvailabilityReport struct {
	Resources map[liquid.ResourceName]ResourceAvailabilityReport `json:"resources"`
}

// ResourceAvailabilityReport contains details about a resource.
// It appears in [CategoryAvailabilityReport].
type ResourceAvailabilityReport struct {
	AvailabilityZones map[limes.AvailabilityZone]uint64 `json:"availability_zones"`
}
