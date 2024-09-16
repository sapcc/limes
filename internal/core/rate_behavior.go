/*******************************************************************************
*
* Copyright 2024 SAP SE
*
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You should have received a copy of the License along with this
* program. If not, you may obtain a copy of the License at
*
*     http://www.apache.org/licenses/LICENSE-2.0
*
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
*
*******************************************************************************/

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
	FullRateNameRx  regexpext.BoundedRegexp `yaml:"rate"`
	IdentityInV1API RateRef                 `yaml:"identity_in_v1_api"`
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
