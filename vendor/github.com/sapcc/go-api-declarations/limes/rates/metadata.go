// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package limesrates

import "github.com/sapcc/go-api-declarations/limes"

// RateName identifies a rate within a service. This type is used to distinguish
// rate names from other types of string values in function signatures.
type RateName string

// RateInfo contains the metadata for a rate (i.e. some type of event that can
// be rate-limited and for which there may a way to retrieve a count of past
// events from a backend service).
type RateInfo struct {
	Name RateName   `json:"name"`
	Unit limes.Unit `json:"unit,omitempty"`
}
