/*******************************************************************************
*
* Copyright 2018 SAP SE
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
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/errext"
	yaml "gopkg.in/yaml.v2"
)

// QuotaConstraintSet contains the contents of the constraint configuration file
// for a limes.Cluster.
type QuotaConstraintSet struct {
	// Indexed by domain name.
	Domains map[string]QuotaConstraints
	// Indexed by domain name, then by project name.
	Projects map[string]map[string]QuotaConstraints
}

// QuotaConstraints contains the quota constraints for a single domain or project.
// The outer key is the service type, the inner key is the resource name.
type QuotaConstraints map[limes.ServiceType]map[limesresources.ResourceName]QuotaConstraint

// QuotaConstraint contains the quota constraints for a single resource within a
// single domain or project.
type QuotaConstraint struct {
	Minimum *uint64
	Maximum *uint64
	Unit    limes.Unit
}

// QuotaValidationError appears in the Limes API in the POST .../simulate-put responses.
type QuotaValidationError struct {
	Status       int        `json:"status"` // an HTTP status code, e.g. http.StatusForbidden
	Message      string     `json:"message"`
	MinimumValue *uint64    `json:"min_acceptable_quota,omitempty"`
	MaximumValue *uint64    `json:"max_acceptable_quota,omitempty"`
	Unit         limes.Unit `json:"unit,omitempty"`
}

func (e *QuotaValidationError) Error() string {
	// Type QuotaUpdater has a function that can return either `error` or
	// `*QuotaValidationError`. That's easier to write down if
	// `*QuotaValidationError` is also `error`, even if I never use it as such.
	panic("DO NOT USE ME")
}

// Validate checks if the given quota value satisfies this constraint, or
// returns an error otherwise.
func (c QuotaConstraint) Validate(value uint64) *QuotaValidationError {
	if (c.Minimum == nil || *c.Minimum <= value) && (c.Maximum == nil || *c.Maximum >= value) {
		return nil
	}
	return &QuotaValidationError{
		Status: http.StatusUnprocessableEntity,
		Message: fmt.Sprintf("requested value %q contradicts constraint %q",
			limes.ValueWithUnit{Value: value, Unit: c.Unit}, c.String(),
		),
		MinimumValue: c.Minimum,
		MaximumValue: c.Maximum,
		Unit:         c.Unit,
	}
}

// ApplyTo modifies the given value such that it satisfies this constraint. If
// c.Allows(value), then the value is returned unchanged.
func (c QuotaConstraint) ApplyTo(value uint64) uint64 {
	if c.Minimum != nil && *c.Minimum > value {
		return *c.Minimum
	}
	if c.Maximum != nil && *c.Maximum < value {
		return *c.Maximum
	}
	return value
}

// String returns a compact string representation of this QuotaConstraint.
// The result is valid input syntax for parseQuotaConstraint().
func (c QuotaConstraint) String() string {
	var parts []string
	hasExactly := false

	if c.Minimum != nil {
		if c.Maximum != nil && *c.Maximum == *c.Minimum {
			parts = append(parts, "exactly "+limes.ValueWithUnit{Value: *c.Minimum, Unit: c.Unit}.String())
			hasExactly = true
		} else {
			parts = append(parts, "at least "+limes.ValueWithUnit{Value: *c.Minimum, Unit: c.Unit}.String())
		}
	}
	if c.Maximum != nil && !hasExactly {
		parts = append(parts, "at most "+limes.ValueWithUnit{Value: *c.Maximum, Unit: c.Unit}.String())
	}
	return strings.Join(parts, ", ")
}

// NewQuotaConstraints parses the quota constraints at `constraintConfigPath`.
// The `cluster` argument is required because quota values need to be converted
// into the base unit of their resource, for which we need to access the
// QuotaPlugin.Resources(). Hence, `cluster.Init()` needs to have been called
// before this function is called.
func NewQuotaConstraints(cluster *Cluster, constraintConfigPath string) (result *QuotaConstraintSet, errs errext.ErrorSet) {
	buf, err := os.ReadFile(constraintConfigPath)
	if err != nil {
		errs.Addf("could not read quota constraints: %w", err)
		return nil, errs
	}

	var data struct {
		// first key is domain name for domains, combined domain and project name for projects
		Domains  map[string]map[limes.ServiceType]map[limesresources.ResourceName]string `yaml:"domains"`
		Projects map[string]map[limes.ServiceType]map[limesresources.ResourceName]string `yaml:"projects"`
	}
	err = yaml.UnmarshalStrict(buf, &data)
	if err != nil {
		errs.Addf("could not parse quota constraints: %w", err)
		return nil, errs
	}

	result = &QuotaConstraintSet{
		Domains:  make(map[string]QuotaConstraints),
		Projects: make(map[string]map[string]QuotaConstraints),
	}

	// parse quota constraints for projects
	for projectAndDomainName, projectData := range data.Projects {
		fields := strings.SplitN(projectAndDomainName, "/", 2)
		if len(fields) < 2 {
			errs.Addf("invalid constraints: missing domain name for project %s", projectAndDomainName)
			continue
		}
		domainName := fields[0]
		projectName := fields[1]

		values, suberrs := compileQuotaConstraints(cluster, projectData, nil)
		for _, err := range suberrs {
			errs.Addf("invalid constraints for project %s: %w", projectAndDomainName, err)
		}

		if _, exists := result.Projects[domainName]; !exists {
			result.Projects[domainName] = make(map[string]QuotaConstraints)
		}
		result.Projects[domainName][projectName] = values
	}

	// parse quota constraints for domains
	for domainName, domainData := range data.Domains {
		// in order to compile "at least X more than project constraints" constraints, we need to give the
		// project constraints for this domain into the compiler
		projectsConstraints := result.Projects[domainName]
		if projectsConstraints == nil {
			projectsConstraints = make(map[string]QuotaConstraints)
		}

		values, suberrs := compileQuotaConstraints(cluster, domainData, projectsConstraints)
		for _, err := range suberrs {
			errs.Addf("invalid constraints for domain %s: %w", domainName, err)
		}
		result.Domains[domainName] = values
	}

	// do not attempt to validate if the parsing already caused errors (a
	// consistent, but invalid constraint set might look inconsistent because
	// values that don't parse were not initialized in `result`)
	if !errs.IsEmpty() {
		return result, errs
	}

	// validate that project quotas fit into domain quotas
	allDomainNames := make(map[string]bool)
	for domainName := range result.Domains {
		allDomainNames[domainName] = true
	}
	for domainName := range result.Projects {
		allDomainNames[domainName] = true
	}
	for domainName := range allDomainNames {
		suberrs := validateQuotaConstraints(cluster, result.Domains[domainName], result.Projects[domainName])
		for _, err := range suberrs {
			errs.Addf("inconsistent constraints for domain %s: %w", domainName, err)
		}
	}

	return result, errs
}

// When `data` contains the constraints for a project, `projectsConstraints` will be nil.
// When `data` contains the constraints for a domain, `projectsConstraints` will be non-nil.
func compileQuotaConstraints(cluster *Cluster, data map[limes.ServiceType]map[limesresources.ResourceName]string, projectsConstraints map[string]QuotaConstraints) (values QuotaConstraints, errs errext.ErrorSet) {
	values = make(QuotaConstraints)

	for serviceType, serviceData := range data {
		if !cluster.HasService(serviceType) {
			// this is not an error: our global constraint sets set quota constraints
			// for all services, but some lab regions do not have all those services
			continue
		}
		values[serviceType] = make(map[limesresources.ResourceName]QuotaConstraint)

		for resourceName, constraintStr := range serviceData {
			if constraintStr == "" {
				continue
			}

			if !cluster.HasResource(serviceType, resourceName) {
				// this is not an error: our global constraint sets have domain quota
				// constraints "at least 0 more than project constraints" for all
				// existing per_flavor instance resources, but we don't have all of
				// these in any region (depending on the regional hardware stock)
				continue
			}
			resource := cluster.InfoForResource(serviceType, resourceName)
			if resource.NoQuota {
				errs.Addf("resource %s/%s does not track quota", serviceType, resourceName)
				continue
			}
			qdConfig := cluster.QuotaDistributionConfigForResource(serviceType, resourceName)
			if projectsConstraints != nil && qdConfig.Model != limesresources.HierarchicalQuotaDistribution {
				errs.Addf("resource %s/%s does not accept domain quota constraints because domain quota is computed automatically according to the %s quota distribution model", serviceType, resourceName, qdConfig.Model)
				continue
			}

			var projectMinimumsSum *uint64
			if projectsConstraints != nil {
				sum := uint64(0)
				for _, projectConstraints := range projectsConstraints {
					minimum := projectConstraints[serviceType][resourceName].Minimum
					if minimum != nil {
						sum += *minimum
					}
				}
				projectMinimumsSum = &sum
			}

			constraint, err := parseQuotaConstraint(resource, constraintStr, projectMinimumsSum)
			if err != nil {
				errs.Addf("invalid constraint %q for %s/%s: %w", constraintStr, serviceType, resourceName, err)
				continue
			}
			if constraint != nil {
				values[serviceType][resourceName] = *constraint
			}
		}
	}

	return values, errs
}

var (
	atLeastRx     = regexp.MustCompile(`^at\s+least\s+(.+)$`)
	atMostRx      = regexp.MustCompile(`^at\s+most\s+(.+)$`)
	exactlyRx     = regexp.MustCompile(`^exactly\s+(.+)$`)
	atLeastMoreRx = regexp.MustCompile(`^at\s+least\s+(.+)\s+more\s+than\s+project\s+constraints$`)
)

// When parsing a constraint for a project, `projectMinimumsSum` will be nil.
// When parsing a constraint for a domain, `projectMinimumsSum` will be non-nil.
func parseQuotaConstraint(resource limesresources.ResourceInfo, str string, projectMinimumsSum *uint64) (*QuotaConstraint, error) {
	var lowerBounds []uint64
	var upperBounds []uint64

	for _, part := range strings.Split(str, ",") {
		part = strings.TrimSpace(part)
		if match := atLeastMoreRx.FindStringSubmatch(part); projectMinimumsSum != nil && match != nil {
			value, err := resource.Unit.Parse(match[1])
			if err != nil {
				return nil, err
			}
			lowerBounds = append(lowerBounds, value+*projectMinimumsSum)
		} else if match := atLeastRx.FindStringSubmatch(part); match != nil {
			value, err := resource.Unit.Parse(match[1])
			if err != nil {
				return nil, err
			}
			lowerBounds = append(lowerBounds, value)
		} else if match := atMostRx.FindStringSubmatch(part); match != nil {
			value, err := resource.Unit.Parse(match[1])
			if err != nil {
				return nil, err
			}
			upperBounds = append(upperBounds, value)
		} else if match := exactlyRx.FindStringSubmatch(part); match != nil {
			value, err := resource.Unit.Parse(match[1])
			if err != nil {
				return nil, err
			}
			lowerBounds = append(lowerBounds, value)
			upperBounds = append(upperBounds, value)
		} else {
			return nil, fmt.Errorf(`clause %q should start with "at least", "at most" or "exactly"`, part)
		}
	}

	result := QuotaConstraint{Unit: resource.Unit}
	pointerTo := func(x uint64) *uint64 { return &x }

	for _, val := range lowerBounds {
		if result.Minimum == nil {
			result.Minimum = pointerTo(val)
		} else if *result.Minimum < val {
			*result.Minimum = val
		}
	}

	for _, val := range upperBounds {
		if result.Maximum == nil {
			result.Maximum = pointerTo(val)
		} else if *result.Maximum > val {
			*result.Maximum = val
		}
	}

	if result.Minimum != nil && result.Maximum != nil && *result.Maximum < *result.Minimum {
		return nil, fmt.Errorf(
			"constraint clauses cannot simultaneously be satisfied (at least %s, but at most %s)",
			limes.ValueWithUnit{Unit: resource.Unit, Value: *result.Minimum},
			limes.ValueWithUnit{Unit: resource.Unit, Value: *result.Maximum},
		)
	}

	// ignore constraints that end up equivalent to "at least 0" (which can happen
	// when a domain constraint is "at least 0 more than project constraints") and
	// then it turns out there are no project constraints for that domain and
	// resource
	if result.Minimum != nil && *result.Minimum == 0 {
		result.Minimum = nil
	}
	if result.Minimum == nil && result.Maximum == nil {
		return nil, nil
	}

	return &result, nil
}

func validateQuotaConstraints(cluster *Cluster, domainConstraints QuotaConstraints, projectsConstraints map[string]QuotaConstraints) (errs errext.ErrorSet) {
	// sum up the constraints of all projects into total min/max quotas
	sumConstraints := make(QuotaConstraints)
	for _, projectConstraints := range projectsConstraints {
		for serviceType, serviceConstraints := range projectConstraints {
			if _, exists := sumConstraints[serviceType]; !exists {
				sumConstraints[serviceType] = make(map[limesresources.ResourceName]QuotaConstraint)
			}
			for resourceName, constraint := range serviceConstraints {
				sumConstraint := sumConstraints[serviceType][resourceName]

				if constraint.Minimum != nil {
					if sumConstraint.Minimum == nil {
						val := *constraint.Minimum
						sumConstraint.Minimum = &val
					} else {
						*sumConstraint.Minimum += *constraint.Minimum
					}
				}
				//NOTE: We're not interested in the Maximum constraints, see below in
				// the checking phase.

				sumConstraints[serviceType][resourceName] = sumConstraint
			}
		}
	}

	// check that sumConstraints fits within the domain constraints (this is only
	// relevant for hierarchical quota distribution; for other quota
	// distribution models, domain quota is auto-computed and constraints are forbidden)
	for serviceType, serviceSums := range sumConstraints {
		for resourceName, sumConstraint := range serviceSums {
			qdConfig := cluster.QuotaDistributionConfigForResource(serviceType, resourceName)
			if qdConfig.Model != limesresources.HierarchicalQuotaDistribution {
				continue
			}

			domainConstraint := domainConstraints[serviceType][resourceName]

			minProjectQuota := uint64(0)
			if sumConstraint.Minimum != nil {
				minProjectQuota = *sumConstraint.Minimum
			}
			minDomainQuota := uint64(0)
			if domainConstraint.Minimum != nil {
				minDomainQuota = *domainConstraint.Minimum
			}

			if minProjectQuota > minDomainQuota {
				unit := cluster.InfoForResource(serviceType, resourceName).Unit
				errs.Addf(
					`sum of "at least/exactly" project quotas (%s) for %s/%s exceeds "at least/exactly" domain quota (%s)`,
					limes.ValueWithUnit{Value: minProjectQuota, Unit: unit},
					serviceType, resourceName,
					limes.ValueWithUnit{Value: minDomainQuota, Unit: unit},
				)
			}
		}
	}

	return
}
