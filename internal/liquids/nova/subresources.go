// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package nova

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/sapcc/go-api-declarations/liquid"
)

type flavorAttributes struct {
	Name           string  `json:"name"`
	VCPUs          uint64  `json:"vcpu"`
	MemoryMiB      uint64  `json:"ram_mib"`
	DiskGiB        uint64  `json:"disk_gib"`
	VideoMemoryMiB *uint64 `json:"video_ram_mib,omitempty"`
	HWVersion      string  `json:"-"` // this is only used for sorting the subresource into the right resource
}

// subresourceAttributes is the Attributes payload for a Nova instance subresource.
type subresourceAttributes struct {
	// base metadata
	Status   string            `json:"status"`
	Metadata map[string]string `json:"metadata"`
	Tags     []string          `json:"tags"`
	// placement information
	AZ liquid.AvailabilityZone `json:"-"`
	// information from flavor
	Flavor flavorAttributes `json:"flavor"`
	// information from image
	OSType string `json:"os_type"`
}

func (l *Logic) buildInstanceSubresource(ctx context.Context, instance servers.Server, allAZs []liquid.AvailabilityZone) (res liquid.SubresourceBuilder[subresourceAttributes], err error) {
	// copy base attributes
	res.ID = instance.ID
	res.Name = instance.Name

	attrs := subresourceAttributes{
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
	attrs.Flavor = flavorAttributes{
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

func (l *Logic) buildInstanceSubresources(ctx context.Context, projectUUID string, allAZs []liquid.AvailabilityZone) ([]liquid.SubresourceBuilder[subresourceAttributes], error) {
	opts := servers.ListOpts{
		AllTenants: true,
		TenantID:   projectUUID,
	}

	var result []liquid.SubresourceBuilder[subresourceAttributes]
	err := foreachServerCustom(ctx, l.NovaV2, opts, func(instance servers.Server) error {
		res, err := l.buildInstanceSubresource(ctx, instance, allAZs)
		if err != nil {
			return fmt.Errorf("while building subresource for instance %s: %w", instance.ID, err)
		}
		result = append(result, res)
		return nil
	})
	return result, err
}

// The performance of liquid-nova is bottlenecked by how quickly it can page through server lists.
// The default implementation of servers.List() in Gophercloud is horrendously inefficient.
// It unmarshals the response body no less than FOUR times:
//
//  1. once into map[string]any during pagination.PageResultFrom() for use in generic NextPageURL/IsEmpty implementations
//     (which do not end up being used)
//  2. once in ServersPage's custom NextPageURL implementation
//  3. once in ServersPage's custom IsEmpty implementation
//  4. once in ServersPage.Extract() to get the actual []servers.Server
//
// The implementation below does the same as servers.List(), but only unmarshals once.
func foreachServerCustom(ctx context.Context, client *gophercloud.ServiceClient, opts servers.ListOpts, action func(servers.Server) error) error {
	query, err := opts.ToServerListQuery()
	if err != nil {
		return err
	}
	url := client.ServiceURL("servers", "detail") + query

	for {
		var result struct {
			Servers []servers.Server   `json:"servers"`
			Links   []gophercloud.Link `json:"servers_links"`
		}
		currentPage, err := client.Get(ctx, url, nil, &gophercloud.RequestOpts{ //nolint:bodyclose // Gophercloud consumes the response body because JSONResponse is given
			OkCodes:      []int{http.StatusOK, http.StatusNoContent},
			JSONResponse: &result,
		})
		if err != nil {
			return err
		}
		if currentPage.StatusCode == http.StatusNoContent || len(result.Servers) == 0 {
			return nil
		}

		for _, server := range result.Servers {
			err = action(server)
			if err != nil {
				return err
			}
		}

		url, err = gophercloud.ExtractNextURL(result.Links)
		if err != nil {
			return err
		}
		if url == "" {
			return nil
		}
	}
}
