// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/errext"
	"github.com/sapcc/go-bits/regexpext"
)

// ResourceBehavior contains the configuration options for specialized
// behaviors of a single resource (or a set thereof).
type ResourceBehavior struct {
	FullResourceNameRx     regexpext.BoundedRegexp `json:"resource"`
	OvercommitFactor       liquid.OvercommitFactor `json:"overcommit_factor"`
	IdentityInV1API        ResourceRef             `json:"identity_in_v1_api"`
	TranslationRuleInV1API TranslationRule         `json:"translation_rule_in_v1_api"`
	Category               string                  `json:"category"`
}

// Validate returns a list of all errors in this behavior configuration.
//
// The `path` argument denotes the location of this behavior in the
// configuration file, and will be used when generating error messages.
func (b *ResourceBehavior) Validate(path string) (errs errext.ErrorSet) {
	if b.FullResourceNameRx == "" {
		errs.Addf("missing configuration value: %s.resource", path)
	}

	return errs
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
}

func interpolateFromNameMatch[S ~string](fullNameRx regexpext.BoundedRegexp, value S, fullName string) S {
	if !strings.Contains(string(value), "$") {
		return value
	}
	rx, err := fullNameRx.Regexp()
	if err != nil {
		// defense in depth: this should not happen because the regex should have been validated at UnmarshalJSON time
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
// When read from the configuration JSON, this deserializes from a string in the "service/resource" or "service/rate" format.
type RefInService[S, R ~string] struct {
	ServiceType S
	Name        R
}

// ResourceRef is an instance of RefInService. It appears in type ResourceBehavior.
type ResourceRef = RefInService[limes.ServiceType, limesresources.ResourceName]

// UnmarshalJSON implements the json.Unmarshaler interface.
func (r *RefInService[S, R]) UnmarshalJSON(data []byte) error {
	var in string
	err := json.Unmarshal(data, &in)
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
