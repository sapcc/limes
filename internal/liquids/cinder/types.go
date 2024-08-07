/*******************************************************************************
*
* Copyright 2017-2024 SAP SE
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

package cinder

import (
	"context"
	"encoding/json"
	"math"

	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/schedulerstats"
	"github.com/sapcc/go-api-declarations/liquid"
)

// VolumeType is a type with convenience functions for deriving resource names.
type VolumeType string

func (vt VolumeType) CapacityResourceName() liquid.ResourceName {
	return liquid.ResourceName("capacity_" + string(vt))
}
func (vt VolumeType) SnapshotsResourceName() liquid.ResourceName {
	return liquid.ResourceName("snapshots_" + string(vt))
}
func (vt VolumeType) VolumesResourceName() liquid.ResourceName {
	return liquid.ResourceName("volumes_" + string(vt))
}

// StoragePool is a custom deserialization target type that replaces
// type schedulerstats.StoragePool.
type StoragePool struct {
	Name         string `json:"name"`
	Capabilities struct {
		VolumeBackendName   string          `json:"volume_backend_name"`
		TotalCapacityGB     float64OrString `json:"total_capacity_gb"`
		AllocatedCapacityGB float64OrString `json:"allocated_capacity_gb"`

		// SAP Converged Cloud extension
		CustomAttributes struct {
			CinderState string `json:"cinder_state"`
		} `json:"custom_attributes"`
	} `json:"capabilities"`
}

func (l *Logic) listStoragePools(ctx context.Context) ([]StoragePool, error) {
	var poolData struct {
		StoragePools []StoragePool `json:"pools"`
	}
	allPages, err := schedulerstats.List(l.CinderV3, schedulerstats.ListOpts{Detail: true}).AllPages(ctx)
	if err != nil {
		return nil, err
	}
	err = allPages.(schedulerstats.StoragePoolPage).ExtractInto(&poolData)
	return poolData.StoragePools, err
}

////////////////////////////////////////////////////////////////////////////////
// OpenStack is being a mess once again

// Used for the "total_capacity_gb" field in Cinder pools, which may be a string like "infinite" or "" (unknown).
type float64OrString float64

// UnmarshalJSON implements the json.Unmarshaler interface.
func (f *float64OrString) UnmarshalJSON(buf []byte) error {
	//ref: <https://github.com/gophercloud/gophercloud/blob/7137f0845e8cf2210601f867e7ddd9f54bb72859/openstack/blockstorage/extensions/schedulerstats/results.go#L60-L74>

	if buf[0] == '"' {
		var str string
		err := json.Unmarshal(buf, &str)
		if err != nil {
			return err
		}

		if str == "infinite" {
			*f = float64OrString(math.Inf(+1))
		} else {
			*f = 0
		}
		return nil
	}

	var val float64
	err := json.Unmarshal(buf, &val)
	*f = float64OrString(val)
	return err
}
