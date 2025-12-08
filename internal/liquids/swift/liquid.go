// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package swift

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	. "github.com/majewsky/gg/option"
	"github.com/majewsky/schwift/v2"
	"github.com/majewsky/schwift/v2/gopherschwift"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/respondwith"
)

// Logic implements the liquidapi.Logic interface for Swift.
type Logic struct {
	// connections
	ResellerAccount *schwift.Account `json:"-"`
}

// Init implements the liquidapi.Logic interface.
func (l *Logic) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	swiftV1, err := openstack.NewObjectStorageV1(provider, eo)
	if err != nil {
		return fmt.Errorf("cannot initialize Swift v1 client: %w", err)
	}
	l.ResellerAccount, err = gopherschwift.Wrap(swiftV1, nil)
	return err
}

// BuildServiceInfo implements the liquidapi.Logic interface.
func (l *Logic) BuildServiceInfo(ctx context.Context) (liquid.ServiceInfo, error) {
	return liquid.ServiceInfo{
		Version: 1,
		Resources: map[liquid.ResourceName]liquid.ResourceInfo{
			"capacity": {
				Unit:        liquid.UnitBytes,
				Topology:    liquid.FlatTopology,
				HasCapacity: false,
				HasQuota:    true,
			},
		},
		UsageMetricFamilies: map[liquid.MetricName]liquid.MetricFamilyInfo{
			"limes_swift_objects_per_container": {
				Type:      liquid.MetricTypeGauge,
				Help:      "Number of objects for each Swift container.",
				LabelKeys: []string{"container_name"},
			},
			"limes_swift_size_bytes_per_container": {
				Type:      liquid.MetricTypeGauge,
				Help:      "Total object size in bytes for each Swift container.",
				LabelKeys: []string{"container_name"},
			},
		},
		QuotaUpdateNeedsProjectMetadata: true,
	}, nil
}

// ScanCapacity implements the liquidapi.Logic interface.
func (l *Logic) ScanCapacity(ctx context.Context, req liquid.ServiceCapacityRequest, serviceInfo liquid.ServiceInfo) (liquid.ServiceCapacityReport, error) {
	// no resources report capacity
	return liquid.ServiceCapacityReport{InfoVersion: serviceInfo.Version}, nil
}

func (l *Logic) account(projectUUID string) *schwift.Account {
	return l.ResellerAccount.SwitchAccount("AUTH_" + projectUUID)
}

func (l *Logic) emptyUsageReport(serviceInfo liquid.ServiceInfo, forbidden bool) liquid.ServiceUsageReport {
	quota := int64(0)
	return liquid.ServiceUsageReport{
		InfoVersion: serviceInfo.Version,
		Resources: map[liquid.ResourceName]*liquid.ResourceUsageReport{
			"capacity": {
				Forbidden: forbidden,
				Quota:     Some(quota),
				PerAZ:     liquid.InAnyAZ(liquid.AZResourceUsageReport{Usage: 0}),
			},
		},
		Metrics: map[liquid.MetricName][]liquid.Metric{
			"limes_swift_objects_per_container":    {},
			"limes_swift_size_bytes_per_container": {},
		},
	}
}

// ScanUsage implements the liquidapi.Logic interface.
func (l *Logic) ScanUsage(ctx context.Context, projectUUID string, req liquid.ServiceUsageRequest, serviceInfo liquid.ServiceInfo) (liquid.ServiceUsageReport, error) {
	// get account metadata
	account := l.account(projectUUID)
	headers, err := account.Headers(ctx)
	switch {
	case schwift.Is(err, http.StatusNotFound):
		// Swift account does not exist yet, but the Keystone project exists (usually after project creation)
		return l.emptyUsageReport(serviceInfo, false), nil
	case schwift.Is(err, http.StatusGone):
		// Swift account was deleted and not yet reaped (usually right before the Keystone project is deleted)
		return l.emptyUsageReport(serviceInfo, true), nil
	case err != nil:
		// unexpected error
		return liquid.ServiceUsageReport{}, err
	}

	// get quota and usage data from account headers
	quota := int64(headers.BytesUsedQuota().Get()) //nolint:gosec // uint64 -> int64 would only fail if quota is bigger than 2^63
	if !headers.BytesUsedQuota().Exists() {
		quota = -1
	}
	usage := headers.BytesUsed().Get()

	// collect object count and size metrics per container
	containerInfos, err := account.Containers().CollectDetailed(ctx)
	if err != nil {
		return liquid.ServiceUsageReport{}, fmt.Errorf("cannot list containers: %w", err)
	}
	objectCountMetrics := make([]liquid.Metric, len(containerInfos))
	bytesUsedMetrics := make([]liquid.Metric, len(containerInfos))
	for idx, info := range containerInfos {
		labelValues := []string{info.Container.Name()}
		objectCountMetrics[idx] = liquid.Metric{
			Value:       float64(info.ObjectCount),
			LabelValues: labelValues,
		}
		bytesUsedMetrics[idx] = liquid.Metric{
			Value:       float64(info.BytesUsed),
			LabelValues: labelValues,
		}
	}

	return liquid.ServiceUsageReport{
		InfoVersion: serviceInfo.Version,
		Resources: map[liquid.ResourceName]*liquid.ResourceUsageReport{
			"capacity": {
				Quota: Some(quota),
				PerAZ: liquid.InAnyAZ(liquid.AZResourceUsageReport{Usage: usage}),
			},
		},
		Metrics: map[liquid.MetricName][]liquid.Metric{
			"limes_swift_objects_per_container":    objectCountMetrics,
			"limes_swift_size_bytes_per_container": bytesUsedMetrics,
		},
	}, nil
}

// SetQuota implements the liquidapi.Logic interface.
func (l *Logic) SetQuota(ctx context.Context, projectUUID string, req liquid.ServiceQuotaRequest, serviceInfo liquid.ServiceInfo) error {
	projectMetadata, ok := req.ProjectMetadata.Unpack()
	if !ok {
		return errors.New("projectMetadata is missing")
	}

	quota := req.Resources["capacity"].Quota
	headers := schwift.NewAccountHeaders()
	headers.BytesUsedQuota().Set(quota)
	// this header brought to you by https://github.com/sapcc/swift-addons
	headers.Set("X-account-Project-Domain-Id-Override", projectMetadata.Domain.UUID)

	account := l.account(projectUUID)
	err := account.Update(ctx, headers, nil)
	if schwift.Is(err, http.StatusNotFound) && quota > 0 {
		// account does not exist yet - if there is a non-zero quota, enable it now
		err = account.Create(ctx, headers.ToOpts())
		if err == nil {
			logg.Info("Swift account %s created", projectUUID)
		}
	}
	return err
}

// ReviewCommitmentChange implements the liquidapi.Logic interface.
func (l *Logic) ReviewCommitmentChange(ctx context.Context, req liquid.CommitmentChangeRequest, serviceInfo liquid.ServiceInfo) (liquid.CommitmentChangeResponse, error) {
	err := errors.New("this liquid does not manage commitments")
	return liquid.CommitmentChangeResponse{}, respondwith.CustomStatus(http.StatusBadRequest, err)
}
