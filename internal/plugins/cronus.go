/*******************************************************************************
*
* Copyright 2020 SAP SE
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
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/core"
)

type cronusPlugin struct {
	// connections
	CronusV1 *cronusClient `yaml:"-"`
}

var cronusRates = []limesrates.RateInfo{
	{
		Name: "attachment_size",
		Unit: limes.UnitBytes,
	},
	{
		Name: "data_transfer_in",
		Unit: limes.UnitBytes,
	},
	{
		Name: "data_transfer_out",
		Unit: limes.UnitBytes,
	},
	{
		Name: "recipients",
		Unit: limes.UnitNone,
	},
}

func init() {
	core.QuotaPluginRegistry.Add(func() core.QuotaPlugin { return &cronusPlugin{} })
}

// Init implements the core.QuotaPlugin interface.
func (p *cronusPlugin) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, serviceType limes.ServiceType) (err error) {
	p.CronusV1, err = newCronusClient(provider, eo)
	return err
}

// PluginTypeID implements the core.QuotaPlugin interface.
func (p *cronusPlugin) PluginTypeID() string {
	return "email-aws"
}

// ServiceInfo implements the core.QuotaPlugin interface.
func (p *cronusPlugin) ServiceInfo() core.ServiceInfo {
	return core.ServiceInfo{
		ProductName: "cronus",
		Area:        "email",
	}
}

// Resources implements the core.QuotaPlugin interface.
func (p *cronusPlugin) Resources() map[liquid.ResourceName]liquid.ResourceInfo {
	return nil
}

// Rates implements the core.QuotaPlugin interface.
func (p *cronusPlugin) Rates() []limesrates.RateInfo {
	return cronusRates
}

// Scrape implements the core.QuotaPlugin interface.
func (p *cronusPlugin) Scrape(ctx context.Context, project core.KeystoneProject, allAZs []limes.AvailabilityZone) (result map[limesresources.ResourceName]core.ResourceData, serializedMetrics []byte, err error) {
	return nil, nil, nil
}

// SetQuota implements the core.QuotaPlugin interface.
func (p *cronusPlugin) SetQuota(ctx context.Context, project core.KeystoneProject, quotas map[limesresources.ResourceName]uint64) error {
	return nil
}

type cronusState struct {
	PreviousTotals struct {
		AttachmentsSize *big.Int `json:"attachments_size"`
		DataTransferIn  *big.Int `json:"data_transfer_in"`
		DataTransferOut *big.Int `json:"data_transfer_out"`
		Recipients      *big.Int `json:"recipients"`
	} `json:"previous_totals"`
	CurrentPeriod struct {
		StartDate string `json:"start"`
	} `json:"current_period"`
}

// ScrapeRates implements the core.QuotaPlugin interface.
func (p *cronusPlugin) ScrapeRates(ctx context.Context, project core.KeystoneProject, prevSerializedState string) (result map[limesrates.RateName]*big.Int, serializedState string, err error) {
	// decode `prevSerializedState`
	var state cronusState
	if prevSerializedState == "" {
		// on first scrape, start with a default value that causes us to open a new billing period immediately down below
		state.PreviousTotals.AttachmentsSize = big.NewInt(0)
		state.PreviousTotals.DataTransferIn = big.NewInt(0)
		state.PreviousTotals.DataTransferOut = big.NewInt(0)
		state.PreviousTotals.Recipients = big.NewInt(0)
		state.CurrentPeriod.StartDate = "1970-01-01"
	} else {
		err := json.Unmarshal([]byte(prevSerializedState), &state)
		if err != nil {
			return nil, "", fmt.Errorf("cannot decode prevSerializedState: %w", err)
		}
	}

	// get usage for the current billing period
	currentUsage, err := p.CronusV1.GetUsage(ctx, project.UUID, false)
	if err != nil {
		return nil, "", err
	}
	logg.Debug("currentUsage = %#v", currentUsage)

	// if a new billing period has started, add the previous billing period's
	// final tally into `state.PreviousTotals`
	var newSerializedState string
	if state.CurrentPeriod.StartDate == currentUsage.StartDate {
		newSerializedState = prevSerializedState
	} else {
		prevUsage, err := p.CronusV1.GetUsage(ctx, project.UUID, true)
		if err != nil {
			return nil, "", err
		}
		logg.Debug("prevUsage = %#v", prevUsage)
		if state.CurrentPeriod.StartDate != prevUsage.StartDate && state.CurrentPeriod.StartDate != "1970-01-01" {
			return nil, "", fmt.Errorf(
				"cannot start new billing period: expected previous billing period to end by %s, but actually ended %s",
				state.CurrentPeriod.StartDate, prevUsage.StartDate,
			)
		}

		state.PreviousTotals.AttachmentsSize = bigintPlusUint64(state.PreviousTotals.AttachmentsSize, prevUsage.AttachmentsSize)
		state.PreviousTotals.DataTransferIn = bigintPlusUint64(state.PreviousTotals.DataTransferIn, prevUsage.DataTransferIn)
		state.PreviousTotals.DataTransferOut = bigintPlusUint64(state.PreviousTotals.DataTransferOut, prevUsage.DataTransferOut)
		state.PreviousTotals.Recipients = bigintPlusUint64(state.PreviousTotals.Recipients, prevUsage.Recipients)
		state.CurrentPeriod.StartDate = currentUsage.StartDate

		newSerializedStateBytes, err := json.Marshal(state)
		if err != nil {
			return nil, "", fmt.Errorf("cannot serialize new state: %w", err)
		}
		newSerializedState = string(newSerializedStateBytes)
	}

	// obtain the current running totals by adding the current billing period's
	// running tally to the previous totals
	return map[limesrates.RateName]*big.Int{
		"attachment_size":   bigintPlusUint64(state.PreviousTotals.AttachmentsSize, currentUsage.AttachmentsSize),
		"data_transfer_in":  bigintPlusUint64(state.PreviousTotals.DataTransferIn, currentUsage.DataTransferIn),
		"data_transfer_out": bigintPlusUint64(state.PreviousTotals.DataTransferOut, currentUsage.DataTransferOut),
		"recipients":        bigintPlusUint64(state.PreviousTotals.Recipients, currentUsage.Recipients),
	}, newSerializedState, nil
}

func bigintPlusUint64(a *big.Int, u uint64) *big.Int {
	var b big.Int
	b.SetUint64(u)
	var c big.Int
	return c.Add(a, &b)
}

// DescribeMetrics implements the core.QuotaPlugin interface.
func (p *cronusPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	// not used by this plugin
}

// CollectMetrics implements the core.QuotaPlugin interface.
func (p *cronusPlugin) CollectMetrics(ch chan<- prometheus.Metric, project core.KeystoneProject, serializedMetrics []byte) error {
	// not used by this plugin
	return nil
}

////////////////////////////////////////////////////////////////////////////////
// Gophercloud client for Cronus

type cronusClient struct {
	*gophercloud.ServiceClient
}

func newCronusClient(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (*cronusClient, error) {
	serviceType := "email-aws"
	eo.ApplyDefaults(serviceType)

	url, err := provider.EndpointLocator(eo)
	if err != nil {
		return nil, err
	}
	return &cronusClient{
		ServiceClient: &gophercloud.ServiceClient{
			ProviderClient: provider,
			Endpoint:       url,
			Type:           serviceType,
		},
	}, nil
}

type cronusUsage struct {
	AttachmentsSize uint64 `json:"attachments_size"`
	DataTransferIn  uint64 `json:"data_transfer_in"`
	DataTransferOut uint64 `json:"data_transfer_out"`
	Recipients      uint64 `json:"recipients"`
	StartDate       string `json:"start"`
	EndDate         string `json:"end"`
}

func (c cronusClient) GetUsage(ctx context.Context, projectUUID string, previous bool) (cronusUsage, error) {
	url := c.ServiceURL("v1", "usage", projectUUID)
	if previous {
		url += "?prev=true"
	}

	var result gophercloud.Result
	_, result.Err = c.Get(ctx, url, &result.Body, &gophercloud.RequestOpts{ //nolint:bodyclose // already closed by gophercloud
		OkCodes: []int{http.StatusOK},
	})

	var data struct {
		Usage cronusUsage `json:"usage"`
	}
	err := result.ExtractInto(&data)
	return data.Usage, err
}
