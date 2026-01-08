// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package liquidapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/volumeattach"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"

	"github.com/sapcc/go-bits/gophercloudext"
	"github.com/sapcc/go-bits/logg"
)

// OSTypeProber contains logic to determine which OS is likely running on a given instance.
// This information is included in instance subresources for the purpose of billing OS licenses.
// The prober lives in this library because this logic is relevant for both liquid-nova (covering virtual machines) and liquid-ironic (covering baremetal servers).
//
// For instances booted from an image, the image metadata is inspected in Glance.
// In descending order of priority, the OS type is:
//   - "image-unknown", if no valid image reference exists in the instance metadata
//   - "image-deleted", if the image has been deleted since the instance was booted
//   - the value in the "vmware_ostype" attribute on the image metadata, if that field exists and the value is valid
//   - "$TYPE", if the image metadata contains a tag of the form "ostype:$TYPE"
//   - "unknown", if no other rule matches
//
// For instances booted using a Cinder volume as root disk, the volume metadata is inspected in Cinder.
// The prober looks for a volume attachment to "/dev/sda" or "/dev/vda".
// In descending order of priority, the OS type is:
//   - "rootdisk-missing", if the boot volume has an empty ID
//   - "rootdisk-inspect-error", if the boot volume cannot be located or if its metadata cannot be inspected in Glance
//   - the value in the "volume_image_metadata.vmware_ostype" attribute on the volume metadata, if that field exists and the value is valid
//   - "unknown", if no other rule matches
type OSTypeProber struct {
	// caches for repeated queries
	CacheByImage    map[string]string // for instances booted from images
	CacheByInstance map[string]string // for instances booted from volumes

	// connections
	NovaV2   *gophercloud.ServiceClient
	CinderV3 *gophercloud.ServiceClient
	GlanceV2 *gophercloud.ServiceClient
}

// NewOSTypeProber builds an OSTypeProber.
func NewOSTypeProber(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (*OSTypeProber, error) {
	novaV2, err := openstack.NewComputeV2(provider, eo)
	if err != nil {
		return nil, err
	}
	novaV2.Microversion = "2.61" // to include extra specs in flavors.ListDetail()

	cinderV3, err := openstack.NewBlockStorageV3(provider, eo)
	if err != nil {
		return nil, err
	}

	glanceV2, err := openstack.NewImageV2(provider, eo)
	if err != nil {
		return nil, err
	}

	return &OSTypeProber{
		CacheByImage:    make(map[string]string),
		CacheByInstance: make(map[string]string),
		NovaV2:          novaV2,
		CinderV3:        cinderV3,
		GlanceV2:        glanceV2,
	}, nil
}

// Get returns the OSType for the given instance.
func (p *OSTypeProber) Get(ctx context.Context, instance servers.Server) string {
	if instance.Image == nil {
		return p.getFromBootVolume(ctx, instance.ID)
	} else {
		return p.getFromImage(ctx, instance.Image["id"])
	}
}

func (p *OSTypeProber) getFromBootVolume(ctx context.Context, instanceID string) string {
	result, ok := p.CacheByInstance[instanceID]
	if ok {
		return result
	}

	result, err := p.findFromBootVolume(ctx, instanceID)
	if err == nil {
		p.CacheByInstance[instanceID] = result
		return result
	} else {
		logg.Error("error while trying to find OS type for instance %s from boot volume: %s", instanceID, gophercloudext.UnpackError(err).Error())
		return "rootdisk-inspect-error"
	}
}

func (p *OSTypeProber) getFromImage(ctx context.Context, imageIDAttribute any) string {
	imageID, ok := imageIDAttribute.(string)
	if !ok {
		// malformed "image" section in the instance JSON returned by Nova
		return "image-missing"
	}

	result, ok := p.CacheByImage[imageID]
	if ok {
		return result
	}

	result, err := p.findFromImage(ctx, imageID)
	if err == nil {
		p.CacheByImage[imageID] = result
		return result
	} else {
		logg.Error("error while trying to find OS type for image %s: %s", imageID, gophercloudext.UnpackError(err).Error())
		return "image-inspect-error"
	}
}

func (p *OSTypeProber) findFromBootVolume(ctx context.Context, instanceID string) (string, error) {
	// list volume attachments
	page, err := volumeattach.List(p.NovaV2, instanceID).AllPages(ctx)
	if err != nil {
		return "", err
	}
	attachments, err := volumeattach.ExtractVolumeAttachments(page)
	if err != nil {
		return "", err
	}

	// find root volume among attachments
	var rootVolumeID string
	for _, attachment := range attachments {
		if attachment.Device == "/dev/sda" || attachment.Device == "/dev/vda" {
			rootVolumeID = attachment.VolumeID
			break
		}
	}
	if rootVolumeID == "" {
		return "rootdisk-missing", nil
	}

	// check volume metadata
	var volume struct {
		ImageMetadata struct {
			VMwareOSType string `json:"vmware_ostype"`
		} `json:"volume_image_metadata"`
	}
	err = volumes.Get(ctx, p.CinderV3, rootVolumeID).ExtractInto(&volume)
	if err != nil {
		return "", err
	}

	if isValidVMwareOSType[volume.ImageMetadata.VMwareOSType] {
		return volume.ImageMetadata.VMwareOSType, nil
	}
	return "unknown", nil
}

func (p *OSTypeProber) findFromImage(ctx context.Context, imageID string) (string, error) {
	var result struct {
		Tags         []string `json:"tags"`
		VMwareOSType string   `json:"vmware_ostype"`
	}
	err := images.Get(ctx, p.GlanceV2, imageID).ExtractInto(&result)
	if err != nil {
		// report a dummy value if image has been deleted...
		if gophercloud.ResponseCodeIs(err, http.StatusNotFound) {
			return "image-deleted", nil
		}
		// otherwise, try to GET image again during next scrape
		return "", err
	}

	// prefer vmware_ostype attribute since this is validated by Nova upon booting the VM
	if isValidVMwareOSType[result.VMwareOSType] {
		return result.VMwareOSType, nil
	}
	// look for a tag like "ostype:rhel7" or "ostype:windows8Server64"
	osType := ""
	for _, tag := range result.Tags {
		if osTypeWithoutPrefix, exists := strings.CutPrefix(tag, "ostype:"); exists {
			if osType == "" {
				osType = osTypeWithoutPrefix
			} else {
				// multiple such tags -> wtf
				osType = ""
				break
			}
		}
	}

	// report "unknown" as a last resort
	if osType == "" {
		return "unknown", nil
	}
	return osType, nil
}
