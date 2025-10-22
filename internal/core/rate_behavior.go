// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	"github.com/sapcc/go-bits/errext"
	"github.com/sapcc/go-bits/regexpext"
)

// RateBehavior contains the configuration options for specialized behavior of
// a single rate (or a set thereof).
type RateBehavior struct {
	FullRateNameRx  regexpext.BoundedRegexp `json:"rate"`
	IdentityInV1API RateRef                 `json:"identity_in_v1_api"`
}

// Validate returns a list of all errors in this behavior configuration.
//
// The `path` argument denotes the location of this behavior in the
// configuration file, and will be used when generating error messages.
func (b *RateBehavior) Validate(path string) (errs errext.ErrorSet) {
	if b.FullRateNameRx == "" {
		errs.Addf("missing configuration value: %s.rate", path)
	}

	return errs
}

// RateRef is an instance of RefInService. It appears in type RateBehavior.
type RateRef = RefInService[limes.ServiceType, limesrates.RateName]

// Merge computes the union of both given resource behaviors.
func (b *RateBehavior) Merge(other RateBehavior, fullRateName string) {
	if other.IdentityInV1API != (RateRef{}) {
		b.IdentityInV1API.ServiceType = interpolateFromNameMatch(other.FullRateNameRx, other.IdentityInV1API.ServiceType, fullRateName)
		b.IdentityInV1API.Name = interpolateFromNameMatch(other.FullRateNameRx, other.IdentityInV1API.Name, fullRateName)
	}
}
