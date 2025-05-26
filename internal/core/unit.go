// SPDX-FileCopyrightText: 2018 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
)

// ConvertUnitFor works like ConvertTo, but instead of taking a unit as an
// argument, it uses the native unit of the specified resource. In contrast to
// ConvertTo(), this also handles UnitUnspecified. Values with unspecified unit
// will be interpreted as being in the native unit, and will not be converted.
func ConvertUnitFor(serviceInfo liquid.ServiceInfo, resourceName liquid.ResourceName, v limes.ValueWithUnit) (uint64, error) {
	if v.Unit == limes.UnitUnspecified {
		return v.Value, nil
	}
	targetUnit := InfoForResource(serviceInfo, resourceName).Unit
	result, err := v.ConvertTo(targetUnit)
	return result.Value, err
}
