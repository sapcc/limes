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

package limes

import (
	"errors"
	"fmt"
	"io/ioutil"
	"regexp"
	"strings"

	yaml "gopkg.in/yaml.v2"
)

//QuotaConstraintSet contains the contents of the constraint configuration file
//for a limes.Cluster.
type QuotaConstraintSet struct {
	//Indexed by domain name.
	Domains map[string]QuotaConstraints
	//Indexed by domain name, then by project name.
	Projects map[string]map[string]QuotaConstraints
}

//QuotaConstraints contains the quota constraints for a single domain or project.
//The outer key is the service type, the inner key is the resource name.
type QuotaConstraints map[string]map[string]QuotaConstraint

//QuotaConstraint contains the quota constraints for a single resource within a
//single domain or project.
type QuotaConstraint struct {
	Minimum  *uint64
	Maximum  *uint64
	Expected *uint64 //TODO: remove (undocumented and used only during transition from quota seeds to quota constraints)
}

//InitialQuotaValue shall be replaced by direct access to Minimum when Expected is removed. (TODO)
func (c QuotaConstraint) InitialQuotaValue() uint64 {
	if c.Minimum != nil {
		return *c.Minimum
	}
	if c.Expected != nil {
		return *c.Expected
	}
	return 0
}

//Allows checks whether the given quota value satisfies this constraint.
func (c QuotaConstraint) Allows(value uint64) bool {
	return (c.Minimum == nil || *c.Minimum <= value) && (c.Maximum == nil || *c.Maximum >= value)
}

//ApplyTo modifies the given value such that it satisfies this constraint. If
//c.Allows(value), then the value is returned unchanged.
func (c QuotaConstraint) ApplyTo(value uint64) uint64 {
	if c.Minimum != nil && *c.Minimum > value {
		return *c.Minimum
	}
	if c.Maximum != nil && *c.Maximum < value {
		return *c.Maximum
	}
	return value
}

//ToString returns a compact string representation of this QuotaConstraint.
//The result is valid input syntax for parseQuotaConstraint(). The argument
//is the unit for the resource in question.
func (c QuotaConstraint) ToString(unit Unit) string {
	var parts []string
	hasExactly := false

	if c.Minimum != nil {
		if c.Maximum != nil && *c.Maximum == *c.Minimum {
			parts = append(parts, "exactly "+ValueWithUnit{*c.Minimum, unit}.String())
			hasExactly = true
		} else {
			parts = append(parts, "at least "+ValueWithUnit{*c.Minimum, unit}.String())
		}
	}
	if c.Maximum != nil && !hasExactly {
		parts = append(parts, "at most "+ValueWithUnit{*c.Maximum, unit}.String())
	}
	if c.Expected != nil {
		parts = append(parts, "should be "+ValueWithUnit{*c.Maximum, unit}.String())
	}
	return strings.Join(parts, ", ")
}

//NewQuotaConstraints parses the quota constraints at `constraintConfigPath`.
//The `cluster` argument is required because quota values need to be converted
//into the base unit of their resource, for which we need to access the
//QuotaPlugin.Resources(). Hence, `cluster.Init()` needs to have been called
//before this function is called.
func NewQuotaConstraints(cluster *Cluster, constraintConfigPath string) (*QuotaConstraintSet, []error) {
	buf, err := ioutil.ReadFile(constraintConfigPath)
	if err != nil {
		return nil, []error{err}
	}

	var data struct {
		//          dom/proj   srvType    resName
		Domains  map[string]map[string]map[string]string `yaml:"domains"`
		Projects map[string]map[string]map[string]string `yaml:"projects"`
	}
	err = yaml.Unmarshal(buf, &data)
	if err != nil {
		return nil, []error{err}
	}

	result := &QuotaConstraintSet{
		Domains:  make(map[string]QuotaConstraints),
		Projects: make(map[string]map[string]QuotaConstraints),
	}
	var errors []error

	//parse quota constraints for domains
	for domainName, domainData := range data.Domains {
		values, errs := compileQuotaConstraints(cluster, domainData)
		for _, err := range errs {
			errors = append(errors,
				fmt.Errorf("invalid constraints for domain %s: %s", domainName, err.Error()),
			)
		}
		result.Domains[domainName] = values
	}

	//parse quota constraints for projects
	for projectAndDomainName, projectData := range data.Projects {
		fields := strings.SplitN(projectAndDomainName, "/", 2)
		if len(fields) < 2 {
			errors = append(errors,
				fmt.Errorf("missing domain name for project %s", projectAndDomainName),
			)
			continue
		}
		domainName := fields[0]
		projectName := fields[1]

		values, errs := compileQuotaConstraints(cluster, projectData)
		for _, err := range errs {
			errors = append(errors,
				fmt.Errorf("invalid constraints for project %s: %s", projectAndDomainName, err.Error()),
			)
		}

		if _, exists := result.Projects[domainName]; !exists {
			result.Projects[domainName] = make(map[string]QuotaConstraints)
		}
		result.Projects[domainName][projectName] = values
	}

	//do not attempt to validate if the parsing already caused errors (a
	//consistent, but invalid constraint set might look inconsistent because
	//values that don't parse were not initialized in `result`)
	if len(errors) > 0 {
		return result, errors
	}

	//validate that project quotas fit into domain quotas
	allDomainNames := make(map[string]bool)
	for domainName := range result.Domains {
		allDomainNames[domainName] = true
	}
	for domainName := range result.Projects {
		allDomainNames[domainName] = true
	}
	for domainName := range allDomainNames {
		errs := validateQuotaConstraints(cluster, result.Domains[domainName], result.Projects[domainName])
		for _, err := range errs {
			errors = append(errors,
				fmt.Errorf("inconsistent constraints for domain %s: %s", domainName, err.Error()),
			)
		}
	}

	return result, errors
}

func compileQuotaConstraints(cluster *Cluster, data map[string]map[string]string) (values QuotaConstraints, errors []error) {
	values = make(QuotaConstraints)

	for serviceType, serviceData := range data {
		if !cluster.HasService(serviceType) {
			errors = append(errors, fmt.Errorf("no such service: %s", serviceType))
			continue
		}
		values[serviceType] = make(map[string]QuotaConstraint)

		for resourceName, constraintStr := range serviceData {
			resource := cluster.InfoForResource(serviceType, resourceName)
			constraint, err := parseQuotaConstraint(resource, constraintStr)
			if err != nil {
				errors = append(errors, fmt.Errorf("invalid constraint %q for %s/%s: %s", constraintStr, serviceType, resourceName, err.Error()))
				continue
			}
			values[serviceType][resourceName] = *constraint
		}
	}

	return values, errors
}

var atLeastRx = regexp.MustCompile(`^at\s+least\s+(.+)$`)
var atMostRx = regexp.MustCompile(`^at\s+most\s+(.+)$`)
var exactlyRx = regexp.MustCompile(`^exactly\s+(.+)$`)
var shouldBeRx = regexp.MustCompile(`^should\s+be\s+(.+)$`)

func parseQuotaConstraint(resource ResourceInfo, str string) (*QuotaConstraint, error) {
	var lowerBounds []uint64
	var upperBounds []uint64
	var expected []uint64

	for _, part := range strings.Split(str, ",") {
		part = strings.TrimSpace(part)
		if match := atLeastRx.FindStringSubmatch(part); match != nil {
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
		} else if match := shouldBeRx.FindStringSubmatch(part); match != nil {
			value, err := resource.Unit.Parse(match[1])
			if err != nil {
				return nil, err
			}
			expected = append(expected, value)
		} else {
			return nil, fmt.Errorf(`clause %q should start with "at least", "at most" or "exactly"`, part)
		}
	}

	var result QuotaConstraint
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
		return nil, errors.New("constraint clauses cannot simultaneously be satisfied")
	}

	switch len(expected) {
	case 0:
		result.Expected = nil
	case 1:
		result.Expected = pointerTo(expected[0])
	default:
		return nil, errors.New(`cannot have multiple "should be" clauses in one constraint`)
	}

	return &result, nil
}

func validateQuotaConstraints(cluster *Cluster, domainConstraints QuotaConstraints, projectsConstraints map[string]QuotaConstraints) (errors []error) {
	//sum up the constraints of all projects into total min/max quotas
	sumConstraints := make(QuotaConstraints)
	for _, projectConstraints := range projectsConstraints {
		for serviceType, serviceConstraints := range projectConstraints {
			if _, exists := sumConstraints[serviceType]; !exists {
				sumConstraints[serviceType] = make(map[string]QuotaConstraint)
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
				//the checking phase.

				sumConstraints[serviceType][resourceName] = sumConstraint
			}
		}
	}

	//check that sumConstraints fits within the domain constraints
	for serviceType, serviceSums := range sumConstraints {
		for resourceName, sumConstraint := range serviceSums {
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
				errors = append(errors, fmt.Errorf(
					`sum of "at least/exactly" project quotas (%s) for %s/%s exceeds "at least/exactly" domain quota (%s)`,
					ValueWithUnit{minProjectQuota, unit},
					serviceType, resourceName,
					ValueWithUnit{minDomainQuota, unit},
				))
			}
		}
	}

	return
}
