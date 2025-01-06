/*******************************************************************************
*
* Copyright 2024 SAP SE
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

package nova

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/pagination"
	"github.com/sapcc/go-api-declarations/liquid"
)

type FlavorAttributes struct {
	Name           string  `json:"name"`
	VCPUs          uint64  `json:"vcpu"`
	MemoryMiB      uint64  `json:"ram"`
	DiskGiB        uint64  `json:"disk"`
	VideoMemoryMiB *uint64 `json:"video_ram,omitempty"`
	HWVersion      string  `json:"-"` // this is only used for sorting the subresource into the right resource
}

// SubresourceAttributes is the Attributes payload for a Nova instance subresource.
type SubresourceAttributes struct {
	// base metadata
	Status   string            `json:"status"`
	Metadata map[string]string `json:"metadata"`
	Tags     []string          `json:"tags"`
	// placement information
	AZ liquid.AvailabilityZone `json:"-"`
	// information from flavor
	Flavor FlavorAttributes `json:"flavor"`
	// information from image
	OSType string `json:"os_type"`
}

func (l *Logic) buildInstanceSubresource(ctx context.Context, instance servers.Server, allAZs []liquid.AvailabilityZone) (res liquid.SubresourceBuilder[SubresourceAttributes], err error) {
	// copy base attributes
	res.ID = instance.ID
	res.Name = instance.Name

	attrs := SubresourceAttributes{
		Status:   instance.Status,
		AZ:       liquid.NormalizeAZ(instance.AvailabilityZone, allAZs),
		Metadata: instance.Metadata,
	}
	if instance.Tags != nil {
		attrs.Tags = *instance.Tags
	}

	// flavor data is given to us as a map[string]any, but we want something more structured
	buf, err := json.Marshal(instance.Flavor)
	if err != nil {
		return res, fmt.Errorf("could not reserialize flavor data for instance %s: %w", instance.ID, err)
	}
	var flavorInfo FlavorInfo
	err = json.Unmarshal(buf, &flavorInfo)
	if err != nil {
		return res, fmt.Errorf("could not parse flavor data for instance %s: %w", instance.ID, err)
	}

	// copy attributes from flavor data
	attrs.Flavor = FlavorAttributes{
		Name:      flavorInfo.OriginalName,
		VCPUs:     flavorInfo.VCPUs,
		MemoryMiB: flavorInfo.MemoryMiB,
		DiskGiB:   flavorInfo.DiskGiB,
		HWVersion: flavorInfo.ExtraSpecs["quota:hw_version"],
	}
	if videoRAMStr, exists := flavorInfo.ExtraSpecs["hw_video:ram_max_mb"]; exists {
		videoRAMVal, err := strconv.ParseUint(videoRAMStr, 10, 64)
		if err == nil {
			attrs.Flavor.VideoMemoryMiB = &videoRAMVal
		}
	}

	// calculate classifications based on image data
	attrs.OSType = l.OSTypeProber.Get(ctx, instance)

	res.Attributes = attrs
	return res, nil
}

func (l *Logic) buildInstanceSubresources(ctx context.Context, projectUUID string, allAZs []liquid.AvailabilityZone) ([]liquid.SubresourceBuilder[SubresourceAttributes], error) {
	opts := servers.ListOpts{
		AllTenants: true,
		TenantID:   projectUUID,
	}

	var result []liquid.SubresourceBuilder[SubresourceAttributes]
	err := servers.List(l.NovaV2, opts).EachPage(ctx, func(ctx context.Context, page pagination.Page) (bool, error) {
		var instances []servers.Server
		err := servers.ExtractServersInto(page, &instances)
		if err != nil {
			return false, err
		}

		for _, instance := range instances {
			res, err := l.buildInstanceSubresource(ctx, instance, allAZs)
			if err != nil {
				return false, fmt.Errorf("while building subresource for instance %s: %w", instance.ID, err)
			}
			result = append(result, res)
		}
		return true, nil
	})
	return result, err
}
