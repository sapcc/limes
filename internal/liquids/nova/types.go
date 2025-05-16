// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package nova

// FlavorInfo contains information about a flavor, in the format that appears
// in Nova's GET /servers/:id in the "flavor" key with newer Nova microversions.
type FlavorInfo struct {
	DiskGiB      uint64            `json:"disk"`
	EphemeralGiB uint64            `json:"ephemeral"`
	ExtraSpecs   map[string]string `json:"extra_specs"`
	OriginalName string            `json:"original_name"`
	MemoryMiB    uint64            `json:"ram"`
	SwapMiB      uint64            `json:"swap"`
	VCPUs        uint64            `json:"vcpus"`
}
