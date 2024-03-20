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
	"regexp"
	"strconv"

	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/errext"
	"github.com/sapcc/go-bits/regexpext"
)

// LowPrivilegeRaiseConfiguration contains the configuration options for
// low-privilege quota raising in a certain cluster.
type LowPrivilegeRaiseConfiguration struct {
	Limits struct {
		ForDomains  map[string]map[string]string `yaml:"domains"`
		ForProjects map[string]map[string]string `yaml:"projects"`
	} `yaml:"limits"`
	ExcludeProjectDomainRx regexpext.PlainRegexp `yaml:"except_projects_in_domains"`
	IncludeProjectDomainRx regexpext.PlainRegexp `yaml:"only_projects_in_domains"`
}

// IsAllowedForProjectsIn checks if low-privilege quota raising is enabled by this config
// for the domain with the given name.
func (cfg LowPrivilegeRaiseConfiguration) IsAllowedForProjectsIn(domainName string) bool {
	if cfg.ExcludeProjectDomainRx != "" && cfg.ExcludeProjectDomainRx.MatchString(domainName) {
		return false
	}
	if cfg.IncludeProjectDomainRx == "" {
		return true
	}
	return cfg.IncludeProjectDomainRx.MatchString(domainName)
}

// LowPrivilegeRaiseLimitSet contains the parsed limits for low-privilege quota
// raising in a certain cluster.
type LowPrivilegeRaiseLimitSet struct {
	LimitsForDomains  map[string]map[string]LowPrivilegeRaiseLimit
	LimitsForProjects map[string]map[string]LowPrivilegeRaiseLimit
}

// LowPrivilegeRaiseLimit is a union type for the different ways in which a
// low-privilege raise limit can be specified.
type LowPrivilegeRaiseLimit struct {
	// At most one of these will be non-zero.
	AbsoluteValue                         uint64
	PercentOfClusterCapacity              float64
	UntilPercentOfClusterCapacityAssigned float64
}

func (cfg LowPrivilegeRaiseConfiguration) parse(quotaPlugins map[string]QuotaPlugin) (result LowPrivilegeRaiseLimitSet, errs errext.ErrorSet) {
	var suberrs errext.ErrorSet
	result.LimitsForDomains, suberrs = parseLPRLimits(cfg.Limits.ForDomains, quotaPlugins, "domain")
	errs.Append(suberrs)
	result.LimitsForProjects, suberrs = parseLPRLimits(cfg.Limits.ForProjects, quotaPlugins, "projects")
	errs.Append(suberrs)
	return
}

func parseLPRLimits(inputs map[string]map[string]string, quotaPlugins map[string]QuotaPlugin, scopeType string) (result map[string]map[string]LowPrivilegeRaiseLimit, errs errext.ErrorSet) {
	result = make(map[string]map[string]LowPrivilegeRaiseLimit)

	for srvType, quotaPlugin := range quotaPlugins {
		result[srvType] = make(map[string]LowPrivilegeRaiseLimit)
		for _, res := range quotaPlugin.Resources() {
			input, exists := inputs[srvType][res.Name]
			if !exists {
				continue
			}
			limit, err := parseOneLPRLimit(input, res.Unit, scopeType)
			if err != nil {
				errs.Addf("while parsing %s low-privilege raise limit: %w", scopeType, err)
			}
			result[srvType][res.Name] = limit
		}
	}
	return
}

var (
	percentOfClusterRx              = regexp.MustCompile(`^([0-9.]+)\s*% of cluster capacity$`)
	untilPercentOfClusterAssignedRx = regexp.MustCompile(`^until ([0-9.]+)\s*% of cluster capacity is assigned$`)
)

func parseOneLPRLimit(input string, unit limes.Unit, scopeType string) (LowPrivilegeRaiseLimit, error) {
	match := percentOfClusterRx.FindStringSubmatch(input)
	if match != nil {
		percent, err := parseFloatPercentage(match[1])
		return LowPrivilegeRaiseLimit{
			PercentOfClusterCapacity: percent,
		}, err
	}

	// the "until X% of cluster capacity is assigned" syntax is only allowed for domains
	if scopeType == "domain" {
		match := untilPercentOfClusterAssignedRx.FindStringSubmatch(input)
		if match != nil {
			percent, err := parseFloatPercentage(match[1])
			return LowPrivilegeRaiseLimit{
				UntilPercentOfClusterCapacityAssigned: percent,
			}, err
		}
	}

	rawValue, err := unit.Parse(input)
	return LowPrivilegeRaiseLimit{
		AbsoluteValue: rawValue,
	}, err
}

func parseFloatPercentage(input string) (float64, error) {
	percent, err := strconv.ParseFloat(input, 64)
	if err != nil {
		return 0, err
	}
	if percent < 0 || percent > 100 {
		return 0, fmt.Errorf("value out of range: %s%%", input)
	}
	return percent, nil
}

// Evaluate converts this limit into an absolute value.
func (l LowPrivilegeRaiseLimit) Evaluate(clusterReport limesresources.ClusterResourceReport, oldQuota uint64) uint64 {
	switch {
	case clusterReport.DomainsQuota == nil:
		// defense in depth - we shouldn't be considering LPR limits at all for resources that don't track quota
		return 0
	case l.AbsoluteValue != 0:
		return l.AbsoluteValue
	case l.PercentOfClusterCapacity != 0:
		if clusterReport.Capacity == nil {
			return 0
		}
		percent := l.PercentOfClusterCapacity / 100
		return uint64(percent * float64(*clusterReport.Capacity))
	case l.UntilPercentOfClusterCapacityAssigned != 0:
		if clusterReport.Capacity == nil {
			return 0
		}
		percent := l.UntilPercentOfClusterCapacityAssigned / 100
		otherDomainsQuota := float64(*clusterReport.DomainsQuota - oldQuota)
		maxQuota := percent*float64(*clusterReport.Capacity) - otherDomainsQuota
		if maxQuota < 0 {
			return 0
		}
		return uint64(maxQuota)
	default:
		return 0
	}
}

// IsReversible is true for limits that do not depend on the quotas of other
// domains and projects.
func (l LowPrivilegeRaiseLimit) IsReversible() bool {
	return l.AbsoluteValue != 0 || l.PercentOfClusterCapacity != 0
}
