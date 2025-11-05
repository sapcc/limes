// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package manila

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/sharedfilesystems/apiversions"
	"github.com/gophercloud/gophercloud/v2/openstack/sharedfilesystems/v2/sharetypes"
	"github.com/prometheus/common/model"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/liquidapi"
	"github.com/sapcc/go-bits/promquery"
	"github.com/sapcc/go-bits/respondwith"
)

// Logic implements the liquidapi.Logic interface for Manila.
type Logic struct {
	// configuration
	CapacityCalculation struct {
		CapacityBalance   float64 `json:"capacity_balance"`
		ShareNetworks     uint64  `json:"share_networks"`
		SharesPerPool     uint64  `json:"shares_per_pool"`
		SnapshotsPerShare uint64  `json:"snapshots_per_share"`
		WithSubcapacities bool    `json:"with_subcapacities"`
	} `json:"capacity_calculation"`
	VirtualShareTypes                   []virtualShareType `json:"share_types"`
	PrometheusAPIConfigForAZAwareness   *promquery.Config  `json:"prometheus_api_for_az_awareness"`
	PrometheusAPIConfigForNetappMetrics *promquery.Config  `json:"prometheus_api_for_netapp_metrics"`
	// connections
	ManilaV2      *gophercloud.ServiceClient                                 `json:"-"`
	AZMetrics     *promquery.BulkQueryCache[azMetricsKey, azMetrics]         `json:"-"`
	NetappMetrics *promquery.BulkQueryCache[netappMetricsKey, netappMetrics] `json:"-"`
	// caches
	ShareTypeIDByName liquidapi.State[map[realShareType]string] `json:"-"`
}

// Init implements the liquidapi.Logic interface.
func (l *Logic) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	if len(l.VirtualShareTypes) == 0 {
		return errors.New("missing required configuration field: share_types")
	}
	if l.CapacityCalculation.ShareNetworks == 0 {
		return errors.New("missing required configuration field: capacity_calculation.share_networks")
	}
	if l.CapacityCalculation.SharesPerPool == 0 {
		return errors.New("missing required configuration field: capacity_calculation.shares_per_pool")
	}
	if l.CapacityCalculation.SnapshotsPerShare == 0 {
		return errors.New("missing required configuration field: capacity_calculation.snapshots_per_share")
	}

	// initialize connection to Manila
	gophercloud.ServiceTypeAliases["shared-file-system"] = []string{"sharev2"}
	l.ManilaV2, err = openstack.NewSharedFileSystemV2(provider, eo)
	if err != nil {
		return err
	}

	microversion, err := l.findMicroversion(ctx, l.ManilaV2)
	if err != nil {
		return err
	}
	if microversion == 0 {
		return errors.New(`cannot find API microversion: no version of the form "2.x" found in advertisement`)
	}
	if microversion < 53 {
		return fmt.Errorf("need at least Manila microversion 2.53 (for replica quotas), but got 2.%d", microversion)
	}
	l.ManilaV2.Microversion = "2.53"

	// initialize connection to Prometheus
	if l.PrometheusAPIConfigForAZAwareness != nil && l.PrometheusAPIConfigForAZAwareness.ServerURL != "" {
		promClientForAZAwareness, err := l.PrometheusAPIConfigForAZAwareness.Connect()
		if err != nil {
			return err
		}
		l.AZMetrics = promquery.NewBulkQueryCache(azMetricsQueries, 2*time.Minute, promClientForAZAwareness)
	}

	if l.PrometheusAPIConfigForNetappMetrics != nil && l.PrometheusAPIConfigForNetappMetrics.ServerURL != "" {
		promClientForNetappMetrics, err := l.PrometheusAPIConfigForNetappMetrics.Connect()
		if err != nil {
			return err
		}
		l.NetappMetrics = promquery.NewBulkQueryCache(netappMetricsQueries, 2*time.Minute, promClientForNetappMetrics)
	}

	return nil
}

func (l *Logic) findMicroversion(ctx context.Context, client *gophercloud.ServiceClient) (int, error) {
	pager, err := apiversions.List(client).AllPages(ctx)
	if err != nil {
		return 0, err
	}
	versions, err := apiversions.ExtractAPIVersions(pager)
	if err != nil {
		return 0, err
	}

	versionRx := regexp.MustCompile(`^2\.(\d+)$`)
	for _, version := range versions {
		match := versionRx.FindStringSubmatch(version.Version)
		if match != nil {
			return strconv.Atoi(match[1])
		}
	}

	// no 2.x version found at all
	return 0, nil
}

// BuildServiceInfo implements the liquidapi.Logic interface.
func (l *Logic) BuildServiceInfo(ctx context.Context) (liquid.ServiceInfo, error) {
	// enumerate all share types to establish an ID-name mapping
	// (the Manila quota API exclusively deals with share type names,
	// but some Prometheus metrics need the share type ID)
	pager, err := sharetypes.List(l.ManilaV2, &sharetypes.ListOpts{IsPublic: "all"}).AllPages(ctx)
	if err != nil {
		return liquid.ServiceInfo{}, fmt.Errorf("cannot enumerate Manila share types: %w", err)
	}
	shareTypes, err := sharetypes.ExtractShareTypes(pager)
	if err != nil {
		return liquid.ServiceInfo{}, fmt.Errorf("cannot unmarshal Manila share types: %w", err)
	}
	shareTypeIDByName := make(map[realShareType]string)
	for _, shareType := range shareTypes {
		shareTypeIDByName[realShareType(shareType.Name)] = shareType.ID
	}
	l.ShareTypeIDByName.Set(shareTypeIDByName)

	// build ResourceInfo set
	topology := liquid.FlatTopology
	if l.AZMetrics != nil {
		topology = liquid.AZAwareTopology
	}
	resInfoForCapacity := liquid.ResourceInfo{
		Unit:                liquid.UnitGibibytes,
		Topology:            topology,
		HasCapacity:         true,
		HasQuota:            true,
		NeedsResourceDemand: true,
	}
	resInfoForSnapmirrorCapacity := liquid.ResourceInfo{
		Unit:                liquid.UnitGibibytes,
		Topology:            topology,
		HasCapacity:         true,
		HasQuota:            false,
		NeedsResourceDemand: true,
	}
	resInfoForObjects := liquid.ResourceInfo{
		Unit:        liquid.UnitNone,
		Topology:    topology,
		HasCapacity: true,
		HasQuota:    true,
	}

	resources := make(map[liquid.ResourceName]liquid.ResourceInfo, 5*len(l.VirtualShareTypes)+1)
	resources["share_networks"] = liquid.ResourceInfo{
		Unit:        liquid.UnitNone,
		Topology:    liquid.FlatTopology,
		HasCapacity: true,
		HasQuota:    true,
	}
	for _, vst := range l.VirtualShareTypes {
		resources[vst.sharesResourceName()] = resInfoForObjects
		resources[vst.snapshotsResourceName()] = resInfoForObjects
		resources[vst.shareCapacityResourceName()] = resInfoForCapacity
		resources[vst.snapshotCapacityResourceName()] = resInfoForCapacity
		if l.NetappMetrics != nil {
			resources[vst.snapmirrorCapacityResourceName()] = resInfoForSnapmirrorCapacity
		}
	}

	return liquid.ServiceInfo{
		Version:                         time.Now().Unix(),
		Resources:                       resources,
		UsageReportNeedsProjectMetadata: true,
		QuotaUpdateNeedsProjectMetadata: true,
	}, nil
}

// ReviewCommitmentChange implements the liquidapi.Logic interface.
func (l *Logic) ReviewCommitmentChange(ctx context.Context, req liquid.CommitmentChangeRequest, serviceInfo liquid.ServiceInfo) (liquid.CommitmentChangeResponse, error) {
	err := errors.New("this liquid does not manage commitments")
	return liquid.CommitmentChangeResponse{}, respondwith.CustomStatus(http.StatusBadRequest, err)
}

////////////////////////////////////////////////////////////////////////////////
// Prometheus queries

const (
	// NOTE: In these queries, the `last_over_time(...[15m])` part guards against temporary unavailability of metrics resulting in spurious zero values.

	// queries for AZ awareness metrics
	shareCountQuery       = `count by (availability_zone_name, project_id, share_type_id) (max by (availability_zone_name, id, project_id, share_id, share_type_id) (last_over_time(openstack_manila_replicas_count_gauge[15m])))`
	shareCapacityQuery    = `sum   by (availability_zone_name, project_id, share_type_id) (max by (availability_zone_name, id, project_id, share_id, share_type_id) (last_over_time(openstack_manila_replicas_size_gauge[15m])))`
	snapshotCountQuery    = `count by (availability_zone_name, project_id, share_type_id) (max by (availability_zone_name, id, project_id, share_id, share_type_id) (last_over_time(openstack_manila_snapshot_count_gauge[15m])))`
	snapshotCapacityQuery = `sum   by (availability_zone_name, project_id, share_type_id) (max by (availability_zone_name, id, project_id, share_id, share_type_id) (last_over_time(openstack_manila_snapshot_size_gauge[15m])))`

	// queries for netapp-exporter metrics
	sharePhysicalUsageQuery      = `sum by (availability_zone, project_id, share_type) (max by (availability_zone, project_id, share_id, share_type) (last_over_time(netapp_volume_used_bytes         {project_id!="",share_type!="",volume_type!="dp",volume_state="online"}[15m])))`
	snapshotPhysicalUsageQuery   = `sum by (availability_zone, project_id, share_type) (max by (availability_zone, project_id, share_id, share_type) (last_over_time(netapp_volume_snapshot_used_bytes{project_id!="",share_type!="",volume_type!="dp",volume_state="online"}[15m])))`
	snapmirrorUsageQuery         = `sum by (availability_zone, project_id, share_type) (netapp_snapmirror_capacity_total_bytes_for_limes)`
	snapmirrorPhysicalUsageQuery = `sum by (availability_zone, project_id, share_type) (netapp_snapmirror_capacity_used_bytes_for_limes)`
)

type azMetricsKey struct {
	AvailabilityZone liquid.AvailabilityZone
	ProjectUUID      string
	ShareTypeID      string
}

func azMetricsKeyer(sample *model.Sample) azMetricsKey {
	return azMetricsKey{
		AvailabilityZone: liquid.AvailabilityZone(sample.Metric["availability_zone_name"]),
		ProjectUUID:      string(sample.Metric["project_id"]),
		ShareTypeID:      string(sample.Metric["share_type_id"]),
	}
}

type netappMetricsKey struct {
	AvailabilityZone liquid.AvailabilityZone
	ProjectUUID      string
	ShareTypeName    realShareType
}

func netappMetricsKeyer(sample *model.Sample) netappMetricsKey {
	return netappMetricsKey{
		AvailabilityZone: liquid.AvailabilityZone(sample.Metric["availability_zone"]),
		ProjectUUID:      string(sample.Metric["project_id"]),
		ShareTypeName:    realShareType(sample.Metric["share_type"]),
	}
}

type azMetrics struct {
	ShareCount          uint64
	ShareCapacityGiB    uint64
	SnapshotCount       uint64
	SnapshotCapacityGiB uint64
}

type netappMetrics struct {
	// all in GiB
	SharePhysicalUsage      uint64
	SnapshotPhysicalUsage   uint64
	SnapmirrorUsage         uint64
	SnapmirrorPhysicalUsage uint64
}

var (
	azMetricsQueries = []promquery.BulkQuery[azMetricsKey, azMetrics]{
		{
			Query:       shareCountQuery,
			Description: "shares usage data",
			Keyer:       azMetricsKeyer,
			Filler: func(entry *azMetrics, sample *model.Sample) {
				entry.ShareCount = roundUp(float64(sample.Value))
			},
		},
		{
			Query:       shareCapacityQuery,
			Description: "share_capacity usage data",
			Keyer:       azMetricsKeyer,
			Filler: func(entry *azMetrics, sample *model.Sample) {
				entry.ShareCapacityGiB = roundUp(float64(sample.Value))
			},
		},
		{
			Query:       snapshotCountQuery,
			Description: "share_snapshots usage data",
			Keyer:       azMetricsKeyer,
			Filler: func(entry *azMetrics, sample *model.Sample) {
				entry.SnapshotCount = roundUp(float64(sample.Value))
			},
			ZeroResultsIsNotAnError: true, // some regions legitimately do not have any snapshots
		},
		{
			Query:       snapshotCapacityQuery,
			Description: "snapshot_capacity usage data",
			Keyer:       azMetricsKeyer,
			Filler: func(entry *azMetrics, sample *model.Sample) {
				entry.SnapshotCapacityGiB = roundUp(float64(sample.Value))
			},
			ZeroResultsIsNotAnError: true, // some regions legitimately do not have any snapshots
		},
	}
	netappMetricsQueries = []promquery.BulkQuery[netappMetricsKey, netappMetrics]{
		{
			Query:       sharePhysicalUsageQuery,
			Description: "share_capacity physical usage data",
			Keyer:       netappMetricsKeyer,
			Filler: func(entry *netappMetrics, sample *model.Sample) {
				entry.SharePhysicalUsage = roundUpIntoGigabytes(float64(sample.Value))
			},
		},
		{
			Query:       snapshotPhysicalUsageQuery,
			Description: "snapshot_capacity physical usage data",
			Keyer:       netappMetricsKeyer,
			Filler: func(entry *netappMetrics, sample *model.Sample) {
				entry.SnapshotPhysicalUsage = roundUpIntoGigabytes(float64(sample.Value))
			},
			ZeroResultsIsNotAnError: true, // some regions legitimately do not have any snapshots
		},
		{
			Query:       snapmirrorUsageQuery,
			Description: "snapmirror_capacity usage data",
			Keyer:       netappMetricsKeyer,
			Filler: func(entry *netappMetrics, sample *model.Sample) {
				entry.SnapmirrorUsage = roundUpIntoGigabytes(float64(sample.Value))
			},
			ZeroResultsIsNotAnError: true, // some regions legitimately do not have any snapmirror deployments
		},
		{
			Query:       snapmirrorPhysicalUsageQuery,
			Description: "snapmirror_capacity physical usage data",
			Keyer:       netappMetricsKeyer,
			Filler: func(entry *netappMetrics, sample *model.Sample) {
				entry.SnapmirrorPhysicalUsage = roundUpIntoGigabytes(float64(sample.Value))
			},
			ZeroResultsIsNotAnError: true, // some regions legitimately do not have any snapmirror deployments
		},
	}
)

func roundUp(number float64) uint64 {
	return uint64(math.Ceil(number))
}

func roundUpIntoGigabytes(bytes float64) uint64 {
	return uint64(math.Ceil(bytes / (1 << 30)))
}
