/*******************************************************************************
*
* Copyright 2023 SAP SE
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
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/volumeattach"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	"github.com/sapcc/go-bits/errext"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/util"
)

// OSTypeProber contains the logic for filling the OSType attribute of a Nova instance subresource.
type OSTypeProber struct {
	// caches
	CacheByImage    map[string]string // for instances booted from images
	CacheByInstance map[string]string // for instances booted from volumes
	// connections
	NovaV2   *gophercloud.ServiceClient
	CinderV3 *gophercloud.ServiceClient
	GlanceV2 *gophercloud.ServiceClient
}

// NewOSTypeProber builds an OSTypeProber.
func NewOSTypeProber(novaV2, cinderV3, glanceV2 *gophercloud.ServiceClient) *OSTypeProber {
	return &OSTypeProber{
		CacheByImage:    make(map[string]string),
		CacheByInstance: make(map[string]string),
		NovaV2:          novaV2,
		CinderV3:        cinderV3,
		GlanceV2:        glanceV2,
	}
}

func (p *OSTypeProber) Get(instance Instance) string {
	if instance.Image == nil {
		return p.getFromBootVolume(instance.ID)
	} else {
		return p.getFromImage(instance.Image["id"])
	}
}

func (p *OSTypeProber) getFromBootVolume(instanceID string) string {
	result, ok := p.CacheByInstance[instanceID]
	if ok {
		return result
	}

	result, err := p.findFromBootVolume(instanceID)
	if err == nil {
		p.CacheByInstance[instanceID] = result
		return result
	} else {
		logg.Error("error while trying to find OS type for instance %s from boot volume: %s", instanceID, util.UnpackError(err).Error())
		return "rootdisk-inspect-error"
	}
}

func (p *OSTypeProber) getFromImage(imageIDAttribute any) string {
	imageID, ok := imageIDAttribute.(string)
	if !ok {
		// malformed "image" section in the instance JSON returned by Nova
		return "image-missing"
	}

	result, ok := p.CacheByImage[imageID]
	if ok {
		return result
	}

	result, err := p.findFromImage(imageID)
	if err == nil {
		p.CacheByImage[imageID] = result
		return result
	} else {
		logg.Error("error while trying to find OS type for image %s: %s", imageID, util.UnpackError(err).Error())
		return "image-inspect-error"
	}
}

func (p *OSTypeProber) findFromBootVolume(instanceID string) (string, error) {
	// list volume attachments
	page, err := volumeattach.List(p.NovaV2, instanceID).AllPages()
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
	err = volumes.Get(p.CinderV3, rootVolumeID).ExtractInto(&volume)
	if err != nil {
		return "", err
	}

	if isValidVMwareOSType[volume.ImageMetadata.VMwareOSType] {
		return volume.ImageMetadata.VMwareOSType, nil
	}
	return "unknown", nil
}

func (p *OSTypeProber) findFromImage(imageID string) (string, error) {
	var result struct {
		Tags         []string `json:"tags"`
		VMwareOSType string   `json:"vmware_ostype"`
	}
	err := images.Get(p.GlanceV2, imageID).ExtractInto(&result)
	if err != nil {
		// report a dummy value if image has been deleted...
		if errext.IsOfType[gophercloud.ErrDefault404](err) {
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
		if strings.HasPrefix(tag, "ostype:") {
			if osType == "" {
				osType = strings.TrimPrefix(tag, "ostype:")
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
