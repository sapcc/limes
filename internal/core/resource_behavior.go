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

	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/regexpext"
)

// ResourceBehavior contains the configuration options for specialized
// behaviors of a single resource (or a set thereof).
type ResourceBehavior struct {
	FullResourceNameRx     regexpext.BoundedRegexp            `yaml:"resource"`
	ScopeRx                regexpext.BoundedRegexp            `yaml:"scope"`
	MaxBurstMultiplier     *limesresources.BurstingMultiplier `yaml:"max_burst_multiplier"`
	OvercommitFactor       float64                            `yaml:"overcommit_factor"`
	ScalesWith             ResourceRef                        `yaml:"scales_with"`
	ScalingFactor          float64                            `yaml:"scaling_factor"`
	MinNonZeroProjectQuota uint64                             `yaml:"min_nonzero_project_quota"`
	Annotations            map[string]any                     `yaml:"annotations"`
}

// Validate returns a list of all errors in this behavior configuration. It
// also applies default values. The `path` argument denotes the location of
// this behavior in the configuration file, and will be used when generating
// error messages.
func (b *ResourceBehavior) Validate(path string) (errs []error) {
	if b.FullResourceNameRx == "" {
		errs = append(errs, fmt.Errorf("missing configuration value: %s.resource", path))
	}

	if b.MaxBurstMultiplier != nil && *b.MaxBurstMultiplier < 0 {
		errs = append(errs, fmt.Errorf("%s.max_burst_multiplier may not be negative", path))
	}

	if (b.ScalesWith.ResourceName == "") != (b.ScalingFactor == 0) {
		errs = append(errs, fmt.Errorf("%[1]s.scaling_factor and %[1]s.scales_with are invalid: if one is given, the other must be given too", path))
	}

	return errs
}

// Matches returns whether this ResourceBehavior matches the given resource and scope.
func (b ResourceBehavior) Matches(fullResourceName, scopeName string) bool {
	if !b.FullResourceNameRx.MatchString(fullResourceName) {
		return false
	}
	return scopeName == "" || b.ScopeRx == "" || b.ScopeRx.MatchString(scopeName)
}

// ToScalingBehavior returns the ScalingBehavior for this resource, or nil if
// no scaling has been configured.
func (b ResourceBehavior) ToScalingBehavior() *limesresources.ScalingBehavior {
	if b.ScalesWith.ResourceName == "" {
		return nil
	}
	return &limesresources.ScalingBehavior{
		ScalesWithServiceType:  b.ScalesWith.ServiceType,
		ScalesWithResourceName: b.ScalesWith.ResourceName,
		ScalingFactor:          b.ScalingFactor,
	}
}

// Merge computes the union of both given resource behaviors.
func (b *ResourceBehavior) Merge(other ResourceBehavior) {
	if b.MaxBurstMultiplier == nil || (other.MaxBurstMultiplier != nil && *b.MaxBurstMultiplier > *other.MaxBurstMultiplier) {
		b.MaxBurstMultiplier = other.MaxBurstMultiplier
	}
	if other.OvercommitFactor != 0 {
		b.OvercommitFactor = other.OvercommitFactor
	}
	if other.ScalingFactor != 0 {
		b.ScalesWith = other.ScalesWith
		b.ScalingFactor = other.ScalingFactor
	}
	if b.MinNonZeroProjectQuota < other.MinNonZeroProjectQuota {
		b.MinNonZeroProjectQuota = other.MinNonZeroProjectQuota
	}
	if len(other.Annotations) > 0 && b.Annotations == nil {
		b.Annotations = make(map[string]any)
	}
	for k, v := range other.Annotations {
		b.Annotations[k] = v
	}
}

// ResourceRef contains a pair of service type and resource name. When read
// from the configuration YAML, this deserializes from a string in the
// "service/resource" format.
type ResourceRef struct {
	ServiceType  string
	ResourceName string
}

// UnmarshalYAML implements the yaml.Unmarshaler interface.
func (r *ResourceRef) UnmarshalYAML(unmarshal func(any) error) error {
	var in string
	err := unmarshal(&in)
	if err != nil {
		return err
	}

	fields := strings.Split(in, "/")
	if len(fields) != 2 || fields[0] == "" || fields[1] == "" {
		return fmt.Errorf(`expected scales_with to follow the "service_type/resource_name" format, but got %q`, in)
	}

	*r = ResourceRef{fields[0], fields[1]}
	return nil
}
