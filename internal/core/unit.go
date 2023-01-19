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
	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
)

// ConvertUnitFor works like ConvertTo, but instead of taking a unit as an
// argument, it uses the native unit of the specified resource. In contrast to
// ConvertTo(), this also handles UnitUnspecified. Values with unspecified unit
// will be interpreted as being in the native unit, and will not be converted.
func ConvertUnitFor(cluster *Cluster, serviceType, resourceName string, v limes.ValueWithUnit) (uint64, error) {
	if v.Unit == limes.UnitUnspecified {
		return v.Value, nil
	}
	targetUnit := cluster.InfoForResource(serviceType, resourceName).Unit
	result, err := v.ConvertTo(targetUnit)
	return result.Value, err
}

// LowPrivilegeRaiseLimit is a union type for the different ways in which a
// low-privilege raise limit can be specified.
type LowPrivilegeRaiseLimit struct {
	//At most one of these will be non-zero.
	AbsoluteValue                         uint64
	PercentOfClusterCapacity              float64
	UntilPercentOfClusterCapacityAssigned float64
}

// Evaluate converts this limit into an absolute value.
func (l LowPrivilegeRaiseLimit) Evaluate(clusterReport limesresources.ClusterResourceReport, oldQuota uint64) uint64 {
	switch {
	case clusterReport.DomainsQuota == nil:
		//defense in depth - we shouldn't be considering LPR limits at all for resources that don't track quota
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
