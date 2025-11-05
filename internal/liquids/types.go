// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package liquids

import (
	"encoding/json"
	"math"
)

////////////////////////////////////////////////////////////////////////////////
// OpenStack is being a mess once again

// Float64WithStringErrors is used for the "total_capacity_gb" field in Cinder and Manila pools,
// which may be a string like "infinite", "unknown" or "".
type Float64WithStringErrors float64

// UnmarshalJSON implements the json.Unmarshaler interface.
func (f *Float64WithStringErrors) UnmarshalJSON(buf []byte) error {
	// Ref: <https://github.com/gophercloud/gophercloud/blob/7137f0845e8cf2210601f867e7ddd9f54bb72859/openstack/blockstorage/extensions/schedulerstats/results.go#L60-L74>
	// Ref: <https://github.com/sapcc/manila/blob/688d856f31597ff27f678df6452e2c53aa4008eb/manila/share/drivers/netapp/dataontap/cluster_mode/lib_base.py#L532-L533>

	if buf[0] == '"' {
		var str string
		err := json.Unmarshal(buf, &str)
		if err != nil {
			return err
		}

		if str == "infinite" {
			*f = Float64WithStringErrors(math.Inf(+1))
		} else {
			*f = 0
		}
		return nil
	}

	var val float64
	err := json.Unmarshal(buf, &val)
	*f = Float64WithStringErrors(val)
	return err
}
