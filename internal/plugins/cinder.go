/*******************************************************************************
*
* Copyright 2017 SAP SE
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
	"errors"
	"math/big"
	"slices"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/blockstorage/extensions/quotasets"
	"github.com/gophercloud/gophercloud/openstack/blockstorage/v3/snapshots"
	"github.com/gophercloud/gophercloud/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/pagination"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"

	"github.com/sapcc/limes/internal/core"
)

type cinderPlugin struct {
	// configuration
	VolumeTypes              []string `yaml:"volume_types"`
	WithVolumeSubresources   bool     `yaml:"with_volume_subresources"`
	WithSnapshotSubresources bool     `yaml:"with_snapshot_subresources"`
	// connections
	CinderV3 *gophercloud.ServiceClient `yaml:"-"`
}

func init() {
	core.QuotaPluginRegistry.Add(func() core.QuotaPlugin { return &cinderPlugin{} })
}

// Init implements the core.QuotaPlugin interface.
func (p *cinderPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	if len(p.VolumeTypes) == 0 {
		return errors.New("quota plugin volumev2: missing required configuration field volumev2.volume_types")
	}

	p.CinderV3, err = openstack.NewBlockStorageV3(provider, eo)
	return err
}

// PluginTypeID implements the core.QuotaPlugin interface.
func (p *cinderPlugin) PluginTypeID() string {
	return "volumev2"
}

// ServiceInfo implements the core.QuotaPlugin interface.
func (p *cinderPlugin) ServiceInfo(serviceType limes.ServiceType) limes.ServiceInfo {
	return limes.ServiceInfo{
		Type:        serviceType,
		ProductName: "cinder",
		Area:        "storage",
	}
}

// Resources implements the core.QuotaPlugin interface.
func (p *cinderPlugin) Resources() []limesresources.ResourceInfo {
	result := make([]limesresources.ResourceInfo, 0, 3*len(p.VolumeTypes))
	for _, volumeType := range p.VolumeTypes {
		category := string(p.makeResourceName("volumev2", volumeType))
		result = append(result,
			limesresources.ResourceInfo{
				Name:     p.makeResourceName("capacity", volumeType),
				Unit:     limes.UnitGibibytes,
				Category: category,
			},
			limesresources.ResourceInfo{
				Name:     p.makeResourceName("snapshots", volumeType),
				Unit:     limes.UnitNone,
				Category: category,
			},
			limesresources.ResourceInfo{
				Name:     p.makeResourceName("volumes", volumeType),
				Unit:     limes.UnitNone,
				Category: category,
			},
		)
	}
	return result
}

// Rates implements the core.QuotaPlugin interface.
func (p *cinderPlugin) Rates() []limesrates.RateInfo {
	return nil
}

func (p *cinderPlugin) makeResourceName(kind, volumeType string) limesresources.ResourceName {
	if p.VolumeTypes[0] == volumeType {
		// the resources for the first volume type don't get the volume type suffix
		// for backwards compatibility reasons
		return limesresources.ResourceName(kind)
	}
	return limesresources.ResourceName(kind + "_" + volumeType)
}

type quotaSetField struct {
	Quota int64
	Usage uint64
}

func (f *quotaSetField) UnmarshalJSON(buf []byte) error {
	// The `quota_set` field in the os-quota-sets response is mostly
	// map[string]quotaSetField, but there is also an "id" key containing a
	// string. Skip deserialization of that value.
	if buf[0] == '"' {
		return nil
	}

	var data struct {
		Quota int64  `json:"limit"`
		Usage uint64 `json:"in_use"`
	}
	err := json.Unmarshal(buf, &data)
	if err == nil {
		f.Quota = data.Quota
		f.Usage = data.Usage
	}
	return err
}

func (f quotaSetField) ToResourceData(allAZs []limes.AvailabilityZone) core.ResourceData {
	return core.ResourceData{
		Quota:     f.Quota,
		UsageData: core.InUnknownAZUnlessEmpty(core.UsageData{Usage: f.Usage}).AndZeroInTheseAZs(allAZs),
	}
}

// ScrapeRates implements the core.QuotaPlugin interface.
func (p *cinderPlugin) ScrapeRates(project core.KeystoneProject, prevSerializedState string) (result map[limesrates.RateName]*big.Int, serializedState string, err error) {
	return nil, "", nil
}

// Scrape implements the core.QuotaPlugin interface.
func (p *cinderPlugin) Scrape(project core.KeystoneProject, allAZs []limes.AvailabilityZone) (result map[limesresources.ResourceName]core.ResourceData, _ []byte, err error) {
	var data struct {
		QuotaSet map[string]quotaSetField `json:"quota_set"`
	}
	err = quotasets.GetUsage(p.CinderV3, project.UUID).ExtractInto(&data)
	if err != nil {
		return nil, nil, err
	}

	result = make(map[limesresources.ResourceName]core.ResourceData)
	for _, volumeType := range p.VolumeTypes {
		result[p.makeResourceName("capacity", volumeType)] = data.QuotaSet["gigabytes_"+volumeType].ToResourceData(allAZs)
		result[p.makeResourceName("snapshots", volumeType)] = data.QuotaSet["snapshots_"+volumeType].ToResourceData(allAZs)
		result[p.makeResourceName("volumes", volumeType)] = data.QuotaSet["volumes_"+volumeType].ToResourceData(allAZs)
	}

	//NOTE: We always enumerate subresources, even if we don't end up reporting
	// them, to calculate the AZ breakdown.
	placementForVolumeUUID, err := p.collectVolumeSubresources(project, allAZs, result)
	if err != nil {
		return nil, nil, err
	}
	if p.WithSnapshotSubresources {
		err = p.collectSnapshotSubresources(project, allAZs, placementForVolumeUUID, result)
		if err != nil {
			return nil, nil, err
		}
	}

	return result, nil, nil
}

type cinderVolumePlacement struct {
	VolumeType       string
	AvailabilityZone limes.AvailabilityZone
}

func (p *cinderPlugin) applyFallbacks(placement *cinderVolumePlacement, allAZs []limes.AvailabilityZone) {
	// group subresources with unknown volume types under the default volume type
	if !slices.Contains(p.VolumeTypes, placement.VolumeType) {
		placement.VolumeType = p.VolumeTypes[0]
	}
	if !slices.Contains(allAZs, placement.AvailabilityZone) {
		placement.AvailabilityZone = limes.AvailabilityZoneUnknown
	}
}

type cinderVolumeSubresource struct {
	UUID             string                 `json:"id"`
	Name             string                 `json:"name"`
	Status           string                 `json:"status"`
	Size             limes.ValueWithUnit    `json:"size"`
	AvailabilityZone limes.AvailabilityZone `json:"availability_zone"`
}

type cinderSnapshotSubresource struct {
	UUID       string              `json:"id"`
	Name       string              `json:"name"`
	Status     string              `json:"status"`
	Size       limes.ValueWithUnit `json:"size"`
	VolumeUUID string              `json:"volume_id"`
}

func (p *cinderPlugin) collectVolumeSubresources(project core.KeystoneProject, allAZs []limes.AvailabilityZone, result map[limesresources.ResourceName]core.ResourceData) (placementForVolumeUUID map[string]cinderVolumePlacement, err error) {
	placementForVolumeUUID = make(map[string]cinderVolumePlacement)
	listOpts := volumes.ListOpts{
		AllTenants: true,
		TenantID:   project.UUID,
	}

	err = volumes.List(p.CinderV3, listOpts).EachPage(func(page pagination.Page) (bool, error) {
		vols, err := volumes.ExtractVolumes(page)
		if err != nil {
			return false, err
		}

		for _, volume := range vols {
			placement := cinderVolumePlacement{
				AvailabilityZone: limes.AvailabilityZone(volume.AvailabilityZone),
				VolumeType:       volume.VolumeType,
			}
			p.applyFallbacks(&placement, allAZs)

			res := cinderVolumeSubresource{
				UUID:   volume.ID,
				Name:   volume.Name,
				Status: volume.Status,
				Size: limes.ValueWithUnit{
					Value: uint64(volume.Size),
					Unit:  limes.UnitGibibytes,
				},
				AvailabilityZone: limes.AvailabilityZone(volume.AvailabilityZone),
			}

			az := placement.AvailabilityZone
			if az != limes.AvailabilityZoneUnknown {
				result[p.makeResourceName("capacity", placement.VolumeType)].AddLocalizedUsage(az, res.Size.Value)
				result[p.makeResourceName("volumes", placement.VolumeType)].AddLocalizedUsage(az, 1)
			}
			if p.WithVolumeSubresources {
				usageData := result[p.makeResourceName("volumes", placement.VolumeType)].UsageData[az]
				usageData.Subresources = append(usageData.Subresources, res)
			}
			placementForVolumeUUID[volume.ID] = placement
		}
		return true, nil
	})
	return placementForVolumeUUID, err
}

func (p *cinderPlugin) collectSnapshotSubresources(project core.KeystoneProject, allAZs []limes.AvailabilityZone, placementForVolumeUUID map[string]cinderVolumePlacement, result map[limesresources.ResourceName]core.ResourceData) error {
	listOpts := snapshots.ListOpts{
		AllTenants: true,
		TenantID:   project.UUID,
	}

	err := snapshots.List(p.CinderV3, listOpts).EachPage(func(page pagination.Page) (bool, error) {
		snaps, err := snapshots.ExtractSnapshots(page)
		if err != nil {
			return false, err
		}

		for _, snapshot := range snaps {
			placement := placementForVolumeUUID[snapshot.VolumeID]
			p.applyFallbacks(&placement, allAZs) // only relevant if the volume ID is unknown and we got a zero-initialized struct

			res := cinderSnapshotSubresource{
				UUID:   snapshot.ID,
				Name:   snapshot.Name,
				Status: snapshot.Status,
				Size: limes.ValueWithUnit{
					Value: uint64(snapshot.Size),
					Unit:  limes.UnitGibibytes,
				},
				VolumeUUID: snapshot.VolumeID,
			}

			az := placement.AvailabilityZone
			if az != limes.AvailabilityZoneUnknown {
				result[p.makeResourceName("capacity", placement.VolumeType)].AddLocalizedUsage(az, res.Size.Value)
				result[p.makeResourceName("snapshots", placement.VolumeType)].AddLocalizedUsage(az, 1)
			}
			if p.WithSnapshotSubresources {
				usageData := result[p.makeResourceName("snapshots", placement.VolumeType)].UsageData[az]
				usageData.Subresources = append(usageData.Subresources, res)
			}
		}
		return true, nil
	})
	return err
}

// SetQuota implements the core.QuotaPlugin interface.
func (p *cinderPlugin) SetQuota(project core.KeystoneProject, quotas map[limesresources.ResourceName]uint64) error {
	var requestData struct {
		QuotaSet map[string]uint64 `json:"quota_set"`
	}
	requestData.QuotaSet = make(map[string]uint64)

	for _, volumeType := range p.VolumeTypes {
		quotaCapacity := quotas[p.makeResourceName("capacity", volumeType)]
		requestData.QuotaSet["gigabytes_"+volumeType] = quotaCapacity
		requestData.QuotaSet["gigabytes"] += quotaCapacity

		quotaSnapshots := quotas[p.makeResourceName("snapshots", volumeType)]
		requestData.QuotaSet["snapshots_"+volumeType] = quotaSnapshots
		requestData.QuotaSet["snapshots"] += quotaSnapshots

		quotaVolumes := quotas[p.makeResourceName("volumes", volumeType)]
		requestData.QuotaSet["volumes_"+volumeType] = quotaVolumes
		requestData.QuotaSet["volumes"] += quotaVolumes
	}

	url := p.CinderV3.ServiceURL("os-quota-sets", project.UUID)
	_, err := p.CinderV3.Put(url, requestData, nil, &gophercloud.RequestOpts{OkCodes: []int{200}}) //nolint:bodyclose // already closed by gophercloud
	return err
}

// DescribeMetrics implements the core.QuotaPlugin interface.
func (p *cinderPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	// not used by this plugin
}

// CollectMetrics implements the core.QuotaPlugin interface.
func (p *cinderPlugin) CollectMetrics(ch chan<- prometheus.Metric, project core.KeystoneProject, serializedMetrics []byte) error {
	// not used by this plugin
	return nil
}
