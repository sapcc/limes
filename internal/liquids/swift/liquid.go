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

package swift

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/projects"
	"github.com/majewsky/schwift/v2"
	"github.com/majewsky/schwift/v2/gopherschwift"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/logg"
)

type Logic struct {
	// connections
	IdentityV3      *gophercloud.ServiceClient `json:"-"`
	ResellerAccount *schwift.Account           `json:"-"`
}

// Init implements the liquidapi.Logic interface.
func (l *Logic) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	swiftV1, err := openstack.NewObjectStorageV1(provider, eo)
	if err != nil {
		return fmt.Errorf("cannot initialize Swift v1 client: %w", err)
	}
	l.ResellerAccount, err = gopherschwift.Wrap(swiftV1, nil)
	if err != nil {
		return err
	}

	l.IdentityV3, err = openstack.NewIdentityV3(provider, eo)
	if err != nil {
		return fmt.Errorf("cannot initialize Keystone v3 client: %w", err)
	}
	return nil
}

// BuildServiceInfo implements the liquidapi.Logic interface.
func (l *Logic) BuildServiceInfo(ctx context.Context) (liquid.ServiceInfo, error) {
	return liquid.ServiceInfo{
		Version: 1,
		Resources: map[liquid.ResourceName]liquid.ResourceInfo{
			"capacity": {
				Unit:        limes.UnitBytes,
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
	}, nil
}

// ScanCapacity implements the liquidapi.Logic interface.
func (l *Logic) ScanCapacity(ctx context.Context, req liquid.ServiceCapacityRequest, serviceInfo liquid.ServiceInfo) (liquid.ServiceCapacityReport, error) {
	// no resources report capacity
	return liquid.ServiceCapacityReport{InfoVersion: serviceInfo.Version}, nil
}

func (l *Logic) Account(projectUUID string) *schwift.Account {
	return l.ResellerAccount.SwitchAccount("AUTH_" + projectUUID)
}

func (l *Logic) emptyUsageReport(serviceInfo liquid.ServiceInfo, forbidden bool) liquid.ServiceUsageReport {
	quota := int64(0)
	return liquid.ServiceUsageReport{
		InfoVersion: serviceInfo.Version,
		Resources: map[liquid.ResourceName]*liquid.ResourceUsageReport{
			"capacity": {
				Forbidden: forbidden,
				Quota:     &quota,
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
	account := l.Account(projectUUID)
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
	quota := int64(headers.BytesUsedQuota().Get())
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
				Quota: &quota,
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
	project, err := projects.Get(ctx, l.IdentityV3, projectUUID).Extract()
	if err != nil {
		return fmt.Errorf("while finding project in Keystone: %w", err)
	}

	quota := req.Resources["capacity"].Quota
	headers := schwift.NewAccountHeaders()
	headers.BytesUsedQuota().Set(quota)
	// this header brought to you by https://github.com/sapcc/swift-addons
	headers.Set("X-Account-Project-Domain-Id-Override", project.DomainID)

	account := l.Account(projectUUID)
	err = account.Update(ctx, headers, nil)
	if schwift.Is(err, http.StatusNotFound) && quota > 0 {
		// account does not exist yet - if there is a non-zero quota, enable it now
		err = account.Create(ctx, headers.ToOpts())
		if err == nil {
			logg.Info("Swift Account %s created", projectUUID)
		}
	}
	return err
}
