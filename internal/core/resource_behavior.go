/*******************************************************************************
*
* Copyright 2017 SAP SE
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
	"fmt"
	"strings"
	"time"

	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/errext"
	"github.com/sapcc/go-bits/regexpext"
)

// ResourceBehavior contains the configuration options for specialized
// behaviors of a single resource (or a set thereof).
type ResourceBehavior struct {
	FullResourceNameRx       regexpext.BoundedRegexp             `yaml:"resource"`
	OvercommitFactor         liquid.OvercommitFactor             `yaml:"overcommit_factor"`
	CommitmentDurations      []limesresources.CommitmentDuration `yaml:"commitment_durations"`
	CommitmentMinConfirmDate *time.Time                          `yaml:"commitment_min_confirm_date"`
	CommitmentUntilPercent   *float64                            `yaml:"commitment_until_percent"`
	CommitmentConversion     CommitmentConversion                `yaml:"commitment_conversion"`
	IdentityInV1API          ResourceRef                         `yaml:"identity_in_v1_api"`
	TranslationRuleInV1API   TranslationRule                     `yaml:"translation_rule_in_v1_api"`
	Category                 string                              `yaml:"category"`
}

// Validate returns a list of all errors in this behavior configuration.
//
// The `path` argument denotes the location of this behavior in the
// configuration file, and will be used when generating error messages.
func (b *ResourceBehavior) Validate(path string) (errs errext.ErrorSet) {
	if b.FullResourceNameRx == "" {
		errs.Addf("missing configuration value: %s.resource", path)
	}
	if b.CommitmentUntilPercent != nil {
		if *b.CommitmentUntilPercent > 100 {
			errs.Addf("invalid value: %s.commitment_until_percent may not be bigger than 100", path)
		}
	}

	return errs
}

// ToCommitmentConfig returns the CommitmentConfiguration for this resource,
// or nil if commitments are not allowed on this resource.
func (b ResourceBehavior) ToCommitmentConfig(now time.Time) *limesresources.CommitmentConfiguration {
	if len(b.CommitmentDurations) == 0 {
		return nil
	}
	result := limesresources.CommitmentConfiguration{
		Durations: b.CommitmentDurations,
	}
	if b.CommitmentMinConfirmDate != nil && b.CommitmentMinConfirmDate.After(now) {
		result.MinConfirmBy = &limes.UnixEncodedTime{Time: *b.CommitmentMinConfirmDate}
	}
	return &result
}

// BuildAPIResourceInfo converts a ResourceInfo from LIQUID into the API
// format, using the category mapping in this behavior object.
func (b ResourceBehavior) BuildAPIResourceInfo(resName limesresources.ResourceName, resInfo liquid.ResourceInfo) limesresources.ResourceInfo {
	return limesresources.ResourceInfo{
		Name:     resName,
		Unit:     resInfo.Unit,
		Category: b.Category,
		NoQuota:  !resInfo.HasQuota,
	}
}

// Merge computes the union of both given resource behaviors.
func (b *ResourceBehavior) Merge(other ResourceBehavior, fullResourceName string) {
	if other.OvercommitFactor != 0 {
		b.OvercommitFactor = other.OvercommitFactor
	}
	b.CommitmentDurations = append(b.CommitmentDurations, other.CommitmentDurations...)
	if other.CommitmentMinConfirmDate != nil {
		if b.CommitmentMinConfirmDate == nil || b.CommitmentMinConfirmDate.Before(*other.CommitmentMinConfirmDate) {
			b.CommitmentMinConfirmDate = other.CommitmentMinConfirmDate
		}
	}
	if other.CommitmentUntilPercent != nil {
		if b.CommitmentUntilPercent == nil || *b.CommitmentUntilPercent > *other.CommitmentUntilPercent {
			b.CommitmentUntilPercent = other.CommitmentUntilPercent
		}
	}
	if other.IdentityInV1API != (ResourceRef{}) {
		b.IdentityInV1API.ServiceType = interpolateFromNameMatch(other.FullResourceNameRx, other.IdentityInV1API.ServiceType, fullResourceName)
		b.IdentityInV1API.Name = interpolateFromNameMatch(other.FullResourceNameRx, other.IdentityInV1API.Name, fullResourceName)
	}
	if !other.TranslationRuleInV1API.IsEmpty() {
		b.TranslationRuleInV1API = other.TranslationRuleInV1API
	}
	if other.Category != "" {
		b.Category = interpolateFromNameMatch(other.FullResourceNameRx, other.Category, fullResourceName)
	}
	if other.CommitmentConversion != (CommitmentConversion{}) {
		b.CommitmentConversion = other.CommitmentConversion
	}
}

func interpolateFromNameMatch[S ~string](fullNameRx regexpext.BoundedRegexp, value S, fullName string) S {
	if !strings.Contains(string(value), "$") {
		return value
	}
	rx, err := fullNameRx.Regexp()
	if err != nil {
		// defense in depth: this should not happen because the regex should have been validated at UnmarshalYAML time
		return value
	}
	match := rx.FindStringSubmatchIndex(fullName)
	if match == nil {
		// defense in depth: this should not happen because this is only called after the resource name has already matched
		return value
	}
	return S(rx.ExpandString(nil, string(value), fullName, match))
}

// RefInService contains a pair of service type and resource or rate name.
// When read from the configuration YAML, this deserializes from a string in the "service/resource" or "service/rate" format.
type RefInService[S, R ~string] struct {
	ServiceType S
	Name        R
}

// ResourceRef is an instance of RefInService. It appears in type ResourceBehavior.
type ResourceRef = RefInService[limes.ServiceType, limesresources.ResourceName]

// UnmarshalYAML implements the yaml.Unmarshaler interface.
func (r *RefInService[S, R]) UnmarshalYAML(unmarshal func(any) error) error {
	var in string
	err := unmarshal(&in)
	if err != nil {
		return err
	}

	fields := strings.Split(in, "/")
	if len(fields) != 2 || fields[0] == "" || fields[1] == "" {
		return fmt.Errorf(`expected identity_in_v1_api to follow the "service_type/rate_or_resource_name" format, but got %q`, in)
	}

	*r = RefInService[S, R]{
		ServiceType: S(fields[0]),
		Name:        R(fields[1]),
	}
	return nil
}

type CommitmentConversion struct {
	Identifier string `yaml:"identifier"`
	Weight     uint64 `yaml:"weight"`
}
