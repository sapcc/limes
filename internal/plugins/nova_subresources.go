/*******************************************************************************
*
* Copyright 2017-2023 SAP SE
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

package plugins

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/pagination"
	"github.com/sapcc/go-api-declarations/limes"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/plugins/nova"
)

// A compute instance as shown in our compute/instances subresources.
type novaInstanceSubresource struct {
	// instance identity
	ID   string `json:"id"`
	Name string `json:"name"`
	// base metadata
	Status   string            `json:"status"`
	Metadata map[string]string `json:"metadata"`
	Tags     []string          `json:"tags"`
	// placement information
	AZ             limes.AvailabilityZone `json:"availability_zone"`
	HypervisorType string                 `json:"hypervisor,omitempty"`
	// information from flavor
	FlavorName     string               `json:"flavor"`
	VCPUs          uint64               `json:"vcpu"`
	MemoryMiB      limes.ValueWithUnit  `json:"ram"`
	DiskGiB        limes.ValueWithUnit  `json:"disk"`
	VideoMemoryMiB *limes.ValueWithUnit `json:"video_ram,omitempty"`
	HWVersion      string               `json:"-"` // this is only used for sorting the subresource into the right resource
	// information from image
	OSType string `json:"os_type"`
}

func (p *novaPlugin) buildInstanceSubresource(instance nova.Instance) (res novaInstanceSubresource, err error) {
	// copy base attributes
	res.ID = instance.ID
	res.Name = instance.Name
	res.Status = instance.Status
	res.AZ = limes.AvailabilityZone(instance.AvailabilityZone)
	res.Metadata = instance.Metadata
	if instance.Tags != nil {
		res.Tags = *instance.Tags
	}

	// flavor data is given to us as a map[string]any, but we want something more structured
	buf, err := json.Marshal(instance.Flavor)
	if err != nil {
		return res, fmt.Errorf("could not reserialize flavor data for instance %s: %w", instance.ID, err)
	}
	var flavorInfo nova.FlavorInfo
	err = json.Unmarshal(buf, &flavorInfo)
	if err != nil {
		return res, fmt.Errorf("could not parse flavor data for instance %s: %w", instance.ID, err)
	}

	// copy attributes from flavor data
	res.FlavorName = flavorInfo.OriginalName
	res.VCPUs = flavorInfo.VCPUs
	res.MemoryMiB = limes.ValueWithUnit{
		Value: flavorInfo.MemoryMiB,
		Unit:  limes.UnitMebibytes,
	}
	res.DiskGiB = limes.ValueWithUnit{
		Value: flavorInfo.DiskGiB,
		Unit:  limes.UnitGibibytes,
	}
	if videoRAMStr, exists := flavorInfo.ExtraSpecs["hw_video:ram_max_mb"]; exists {
		videoRAMVal, err := strconv.ParseUint(videoRAMStr, 10, 64)
		if err == nil {
			res.VideoMemoryMiB = &limes.ValueWithUnit{
				Value: videoRAMVal,
				Unit:  limes.UnitMebibytes,
			}
		}
	}
	res.HWVersion = flavorInfo.ExtraSpecs["quota:hw_version"]

	// calculate classifications based on flavor data
	if len(p.HypervisorTypeRules) > 0 {
		res.HypervisorType = p.HypervisorTypeRules.Evaluate(flavorInfo)
	}

	// calculate classifications based on image data
	res.OSType = p.OSTypeProber.Get(instance)
	return res, nil
}

func (p *novaPlugin) buildInstanceSubresources(project core.KeystoneProject) ([]novaInstanceSubresource, error) {
	opts := novaServerListOpts{
		AllTenants: true,
		TenantID:   project.UUID,
	}

	var result []novaInstanceSubresource
	err := servers.List(p.NovaV2, opts).EachPage(func(page pagination.Page) (bool, error) {
		var instances []nova.Instance
		err := servers.ExtractServersInto(page, &instances)
		if err != nil {
			return false, err
		}

		for _, instance := range instances {
			res, err := p.buildInstanceSubresource(instance)
			if err != nil {
				return false, err
			}
			result = append(result, res)
		}
		return true, nil
	})
	return result, err
}

type novaServerListOpts struct {
	AllTenants bool   `q:"all_tenants"`
	TenantID   string `q:"tenant_id"`
}

func (opts novaServerListOpts) ToServerListQuery() (string, error) {
	q, err := gophercloud.BuildQueryString(opts)
	return q.String(), err
}
