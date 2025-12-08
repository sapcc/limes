// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package datamodel

import "github.com/sapcc/go-api-declarations/liquid"

// AZHasQuotaForTopology returns true if the given availability zone can have a quota for the given topology.
// More specifically, for az-separated case, there is no "total" AZ value necessary.
// Unknown never gets a valid value.
func AZHasQuotaForTopology(topology liquid.Topology, az liquid.AvailabilityZone) bool {
	if az == liquid.AvailabilityZoneUnknown {
		return false
	}
	switch topology {
	case liquid.FlatTopology:
		return !az.IsReal()
	case liquid.AZAwareTopology:
		return true
	case liquid.AZSeparatedTopology:
		return az.IsReal()
	default:
		// require an explicit update to this function when new topologies are added
		panic("do not know how to handle topology: " + string(topology))
	}
}

// AZHasBackendQuotaForTopology returns true if the given availability zone can have a backend quota for the given topology.
// This behaves almost similar to AZHasQuotaForTopology, but for non-az-separated topologies, only the "total" AZ value is valid.
// Unknown never gets a valid value.
func AZHasBackendQuotaForTopology(topology liquid.Topology, az liquid.AvailabilityZone) bool {
	switch topology {
	case liquid.FlatTopology, liquid.AZAwareTopology:
		return az == liquid.AvailabilityZoneTotal
	case liquid.AZSeparatedTopology:
		return az.IsReal()
	default:
		// require an explicit update to this function when new topologies are added
		panic("do not know how to handle topology: " + string(topology))
	}
}
