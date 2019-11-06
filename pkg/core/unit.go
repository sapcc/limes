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

import "github.com/sapcc/limes"

//ConvertUnitFor works like ConvertTo, but instead of taking a unit as an
//argument, it uses the native unit of the specified resource. In contrast to
//ConvertTo(), this also handles UnitUnspecified. Values with unspecified unit
//will be interpreted as being in the native unit, and will not be converted.
func ConvertUnitFor(cluster *Cluster, serviceType, resourceName string, v limes.ValueWithUnit) (uint64, error) {
	if v.Unit == limes.UnitUnspecified {
		return v.Value, nil
	}
	targetUnit := cluster.InfoForResource(serviceType, resourceName).Unit
	result, err := v.ConvertTo(targetUnit)
	return result.Value, err
}

//LowPrivilegeRaiseLimit is a union type for the different ways in which a
//low-privilege raise limit can be specified.
type LowPrivilegeRaiseLimit struct {
	//At most one of these will be non-zero.
	AbsoluteValue            uint64
	PercentOfClusterCapacity float64
}

//Evaluate converts this limit into an absolute value.
func (l LowPrivilegeRaiseLimit) Evaluate(getCapacity func() (uint64, error)) (uint64, error) {
	switch {
	case l.AbsoluteValue != 0:
		return l.AbsoluteValue, nil
	case l.PercentOfClusterCapacity != 0:
		capacity, err := getCapacity()
		return uint64(l.PercentOfClusterCapacity / 100 * float64(capacity)), err
	default:
		return 0, nil
	}
}
