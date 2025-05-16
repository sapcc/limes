// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package manila

import (
	"fmt"
	"slices"

	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/regexpext"
)

// RealShareType is a share type name that can be used in the Manila API.
type RealShareType string

// VirtualShareType is the configuration for a virtual share type.
type VirtualShareType struct {
	Name               RealShareType `json:"name"`
	ReplicationEnabled bool          `json:"replication_enabled"` // only used by Usage Collection
	MappingRules       []struct {
		FullProjectNamePattern regexpext.BoundedRegexp `json:"match_project_name"`
		Name                   RealShareType           `json:"name"`
	} `json:"mapping_rules"`
}

func (vst VirtualShareType) SharesResourceName() liquid.ResourceName {
	return liquid.ResourceName("shares_" + string(vst.Name))
}
func (vst VirtualShareType) SnapshotsResourceName() liquid.ResourceName {
	return liquid.ResourceName("snapshots_" + string(vst.Name))
}
func (vst VirtualShareType) ShareCapacityResourceName() liquid.ResourceName {
	return liquid.ResourceName("share_capacity_" + string(vst.Name))
}
func (vst VirtualShareType) SnapshotCapacityResourceName() liquid.ResourceName {
	return liquid.ResourceName("snapshot_capacity_" + string(vst.Name))
}
func (vst VirtualShareType) SnapmirrorCapacityResourceName() liquid.ResourceName {
	return liquid.ResourceName("snapmirror_capacity_" + string(vst.Name))
}

// RealShareTypeIn returns the real share type that we have to use on the Manila
// API for this particular project, or "" if this share type shall be skipped
// for this project.
func (vst VirtualShareType) RealShareTypeIn(project liquid.ProjectMetadata) (rst RealShareType, omit bool) {
	fullName := fmt.Sprintf(`%s@%s`, project.Name, project.Domain.Name)
	for _, rule := range vst.MappingRules {
		if rule.FullProjectNamePattern.MatchString(fullName) {
			return rule.Name, rule.Name == ""
		}
	}
	return vst.Name, false
}

// AllRealShareTypes returns all real share types that can be used on the
// Manila API to set quota or read usage for this virtual share type.
func (vst VirtualShareType) AllRealShareTypes() (result []RealShareType) {
	for _, rule := range vst.MappingRules {
		// rules that make the share type inaccessible should not be considered
		if rule.Name == "" {
			continue
		}

		// only enter unique values into the result
		if !slices.Contains(result, rule.Name) {
			result = append(result, rule.Name)
		}

		// if there is a catch-all rule, no rules afterwards will have any effect
		if rule.FullProjectNamePattern == `.*` || rule.FullProjectNamePattern == `.+` {
			return result
		}
	}

	// if there is no pattern like `.*`, projects that do not match any of the
	// mapping rules will use the name of the virtual share type as the actual
	// Manila-level share type
	if !slices.Contains(result, vst.Name) {
		result = append(result, vst.Name)
	}
	return result
}
