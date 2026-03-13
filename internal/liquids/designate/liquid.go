// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package designate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"text/template"

	"github.com/gophercloud/gophercloud/v2"
	. "github.com/majewsky/gg/option"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/promquery"
	"github.com/sapcc/go-bits/respondwith"
)

// Logic implements the liquidapi.Logic interface for Designate.
type Logic struct {
	// configuration
	PrometheusConfig struct {
		APIConfig promquery.Config `json:"api"`
		Queries   struct {
			Zones             string `json:"zones"`
			RecordsetsPerZone string `json:"recordsets_per_zone"`
		} `json:"queries"`
	} `json:"prometheus_config"`
	// connections
	DesignateV2 *Client `json:"-"`
	Templates   struct {
		Zones             *template.Template `json:"-"`
		RecordsetsPerZone *template.Template `json:"-"`
	}
}

// Init implements the liquidapi.Logic interface.
func (l *Logic) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	l.DesignateV2, err = newClient(provider, eo)
	if err != nil {
		return fmt.Errorf("init designate v2 client: %w", err)
	}
	l.Templates.Zones, err = parseQuery(l.PrometheusConfig.Queries.Zones)
	if err != nil {
		return fmt.Errorf("parse zones query: %w", err)
	}
	l.Templates.RecordsetsPerZone, err = parseQuery(l.PrometheusConfig.Queries.RecordsetsPerZone)
	if err != nil {
		return fmt.Errorf("parse recordsets per zone query: %w", err)
	}
	return err
}

func parseQuery(query string) (tmpl *template.Template, err error) {
	if query == "" {
		return tmpl, errors.New("query is empty")
	}
	tmpl, err = template.New("query").Parse(query)
	if err != nil {
		return tmpl, fmt.Errorf("error while parsing the template: %w", err)
	}
	return tmpl, nil
}

// BuildServiceInfo implements the liquidapi.Logic interface.
func (l *Logic) BuildServiceInfo(ctx context.Context) (liquid.ServiceInfo, error) {
	return liquid.ServiceInfo{
		Version:     2,
		DisplayName: "DNS",
		Resources: map[liquid.ResourceName]liquid.ResourceInfo{
			"zones": {
				DisplayName: "Zones",
				Unit:        liquid.UnitNone,
				Topology:    liquid.FlatTopology,
				HasQuota:    true,
			},
			"recordsets_per_zone": {
				DisplayName: "Recordsets per Zone",
				Unit:        liquid.UnitNone,
				Topology:    liquid.FlatTopology,
				HasQuota:    true,
			},
		},
	}, nil
}

// ScanCapacity implements the liquidapi.Logic interface.
func (l *Logic) ScanCapacity(ctx context.Context, req liquid.ServiceCapacityRequest, serviceInfo liquid.ServiceInfo) (liquid.ServiceCapacityReport, error) {
	// no resources report capacity
	return liquid.ServiceCapacityReport{InfoVersion: serviceInfo.Version}, nil
}

// ScanUsage implements the liquidapi.Logic interface.
func (l *Logic) ScanUsage(ctx context.Context, projectUUID string, req liquid.ServiceUsageRequest, serviceInfo liquid.ServiceInfo) (liquid.ServiceUsageReport, error) {
	// query quotas
	quotas, err := l.DesignateV2.getQuota(ctx, projectUUID)
	if err != nil {
		return liquid.ServiceUsageReport{}, err
	}

	// note: The following data is available via designate API, but we transitioned to use
	// Prometheus queries because of the faster response times for more frequent scraping.
	client, err := l.PrometheusConfig.APIConfig.Connect()
	if err != nil {
		return liquid.ServiceUsageReport{}, fmt.Errorf("while getting prometheus client: %w", err)
	}
	scrapeUsageMetric := func(template *template.Template) (uint64, error) {
		data := map[string]any{
			"ProjectUUID": projectUUID,
		}
		var templated bytes.Buffer
		err = template.Execute(&templated, data)
		if err != nil {
			return 0, fmt.Errorf("error while filling the template: %w", err)
		}
		value, err := client.GetSingleValue(ctx, templated.String(), new(0.0))
		if err != nil {
			return 0, fmt.Errorf("error while retrieving prometheus value: %w", err)
		}
		if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return 0, fmt.Errorf("unexpected value: %f", value)
		}
		return uint64(math.Round(value)), nil
	}

	zones, err := scrapeUsageMetric(l.Templates.Zones)
	if err != nil {
		return liquid.ServiceUsageReport{}, fmt.Errorf("while scraping zones usage: %w", err)
	}
	maxRecordsetsPerZone, err := scrapeUsageMetric(l.Templates.RecordsetsPerZone)
	if err != nil {
		return liquid.ServiceUsageReport{}, fmt.Errorf("while scraping recordsets per zone usage: %w", err)
	}

	return liquid.ServiceUsageReport{
		InfoVersion: serviceInfo.Version,
		Resources: map[liquid.ResourceName]*liquid.ResourceUsageReport{
			"zones": {
				Quota: Some(quotas.Zones),
				PerAZ: liquid.InAnyAZ(liquid.AZResourceUsageReport{
					Usage: zones,
				}),
			},
			"recordsets_per_zone": {
				Quota: Some(quotas.RecordsetsPerZone),
				PerAZ: liquid.InAnyAZ(liquid.AZResourceUsageReport{
					Usage: maxRecordsetsPerZone,
				}),
			},
		},
	}, nil
}

// SetQuota implements the liquidapi.Logic interface.
func (l *Logic) SetQuota(ctx context.Context, projectUUID string, req liquid.ServiceQuotaRequest, serviceInfo liquid.ServiceInfo) error {
	return l.DesignateV2.setQuota(ctx, projectUUID, quotaSet{
		Zones:             int64(req.Resources["zones"].Quota),               //nolint:gosec // uint64 -> int64 would only fail if quota is bigger than 2^63
		RecordsetsPerZone: int64(req.Resources["recordsets_per_zone"].Quota), //nolint:gosec // uint64 -> int64 would only fail if quota is bigger than 2^63

		// Designate has a records_per_recordset quota of default 20, so if we set
		// ZoneRecords to 20 * ZoneRecordsets, this quota will not disturb us
		RecordsPerZone: int64(req.Resources["recordsets_per_zone"].Quota * 20), //nolint:gosec // uint64 -> int64 would only fail if quota is bigger than 2^63 / 20
	})
}

// ReviewCommitmentChange implements the liquidapi.Logic interface.
func (l *Logic) ReviewCommitmentChange(ctx context.Context, req liquid.CommitmentChangeRequest, serviceInfo liquid.ServiceInfo) (liquid.CommitmentChangeResponse, error) {
	err := errors.New("this liquid does not manage commitments")
	return liquid.CommitmentChangeResponse{}, respondwith.CustomStatus(http.StatusBadRequest, err)
}
