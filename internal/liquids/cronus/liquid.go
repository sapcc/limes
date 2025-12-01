// SPDX-FileCopyrightText: 2020 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package cronus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"

	"github.com/gophercloud/gophercloud/v2"
	. "github.com/majewsky/gg/option"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/respondwith"
)

// Logic implements the liquidapi.Logic interface for Cronus.
type Logic struct {
	// connections
	CronusV1 *Client `json:"-"`
}

type Version string

const versionV0 Version = "v0"
const versionV1 Version = "v1"

// Init implements the liquidapi.Logic interface.
func (l *Logic) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	l.CronusV1, err = newClient(provider, eo)
	return err
}

// BuildServiceInfo implements the liquidapi.Logic interface.
func (l *Logic) BuildServiceInfo(ctx context.Context) (liquid.ServiceInfo, error) {
	return liquid.ServiceInfo{
		Version: 1,
		Rates: map[liquid.RateName]liquid.RateInfo{
			"attachment_size":           {HasUsage: true, Topology: liquid.FlatTopology, Unit: liquid.UnitBytes},
			"data_transfer_in":          {HasUsage: true, Topology: liquid.FlatTopology, Unit: liquid.UnitBytes},
			"data_transfer_out":         {HasUsage: true, Topology: liquid.FlatTopology, Unit: liquid.UnitBytes},
			"recipients":                {HasUsage: true, Topology: liquid.FlatTopology, Unit: liquid.UnitNone},
			"messages_sent_aws":         {HasUsage: true, Topology: liquid.FlatTopology, Unit: liquid.UnitNone},
			"messages_received_aws":     {HasUsage: true, Topology: liquid.FlatTopology, Unit: liquid.UnitNone},
			"data_sent_aws":             {HasUsage: true, Topology: liquid.FlatTopology, Unit: liquid.UnitBytes},
			"data_received_aws":         {HasUsage: true, Topology: liquid.FlatTopology, Unit: liquid.UnitBytes},
			"messages_sent_postfix":     {HasUsage: true, Topology: liquid.FlatTopology, Unit: liquid.UnitNone},
			"messages_received_postfix": {HasUsage: true, Topology: liquid.FlatTopology, Unit: liquid.UnitNone},
			"data_sent_postfix":         {HasUsage: true, Topology: liquid.FlatTopology, Unit: liquid.UnitBytes},
			"data_received_postfix":     {HasUsage: true, Topology: liquid.FlatTopology, Unit: liquid.UnitBytes},
		},
	}, nil
}

// ScanCapacity implements the liquidapi.Logic interface.
func (l *Logic) ScanCapacity(ctx context.Context, req liquid.ServiceCapacityRequest, serviceInfo liquid.ServiceInfo) (liquid.ServiceCapacityReport, error) {
	// no resources report capacity
	return liquid.ServiceCapacityReport{InfoVersion: serviceInfo.Version}, nil
}

// The payload format for this liquid's SerializedState.
type cronusState struct {
	PreviousTotals struct {
		AttachmentsSize         *big.Int `json:"attachments_size"`
		DataTransferIn          *big.Int `json:"data_transfer_in"`
		DataTransferOut         *big.Int `json:"data_transfer_out"`
		Recipients              *big.Int `json:"recipients"`
		MessagesSentAWS         *big.Int `json:"messages_sent_aws"`
		MessagesReceivedAWS     *big.Int `json:"messages_received_aws"`
		DataSentAWS             *big.Int `json:"data_sent_aws"`
		DataReceivedAWS         *big.Int `json:"data_received_aws"`
		MessagesSentPostfix     *big.Int `json:"messages_sent_postfix"`
		MessagesReceivedPostfix *big.Int `json:"messages_received_postfix"`
		DataSentPostfix         *big.Int `json:"data_sent_postfix"`
		DataReceivedPostfix     *big.Int `json:"data_received_postfix"`
	} `json:"previous_totals"`
	CurrentPeriod struct {
		StartDate string `json:"start"`
	} `json:"current_period"`
	Version Option[Version] `json:"version"`
}

// ScanUsage implements the liquidapi.Logic interface.
func (l *Logic) ScanUsage(ctx context.Context, projectUUID string, req liquid.ServiceUsageRequest, serviceInfo liquid.ServiceInfo) (liquid.ServiceUsageReport, error) {
	// decode previous SerializedState
	var state cronusState
	if len(req.SerializedState) == 0 {
		// on first scrape, start with a default value that causes us to open a new billing period immediately down below
		state.PreviousTotals.AttachmentsSize = big.NewInt(0)
		state.PreviousTotals.DataTransferIn = big.NewInt(0)
		state.PreviousTotals.DataTransferOut = big.NewInt(0)
		state.PreviousTotals.Recipients = big.NewInt(0)
		state.PreviousTotals.MessagesSentAWS = big.NewInt(0)
		state.PreviousTotals.MessagesReceivedAWS = big.NewInt(0)
		state.PreviousTotals.DataSentAWS = big.NewInt(0)
		state.PreviousTotals.DataReceivedAWS = big.NewInt(0)
		state.PreviousTotals.MessagesSentPostfix = big.NewInt(0)
		state.PreviousTotals.MessagesReceivedPostfix = big.NewInt(0)
		state.PreviousTotals.DataSentPostfix = big.NewInt(0)
		state.PreviousTotals.DataReceivedPostfix = big.NewInt(0)
		state.CurrentPeriod.StartDate = "1970-01-01"
	} else {
		err := json.Unmarshal([]byte(req.SerializedState), &state)
		if err != nil {
			return liquid.ServiceUsageReport{}, fmt.Errorf("cannot decode prevSerializedState: %w", err)
		}
		if state.Version.UnwrapOr(versionV0) == versionV0 {
			// Update v0 -> v1
			state.PreviousTotals.MessagesSentAWS = big.NewInt(0)
			state.PreviousTotals.MessagesReceivedAWS = big.NewInt(0)
			state.PreviousTotals.DataSentAWS = big.NewInt(0)
			state.PreviousTotals.DataReceivedAWS = big.NewInt(0)
			state.PreviousTotals.MessagesSentPostfix = big.NewInt(0)
			state.PreviousTotals.MessagesReceivedPostfix = big.NewInt(0)
			state.PreviousTotals.DataSentPostfix = big.NewInt(0)
			state.PreviousTotals.DataReceivedPostfix = big.NewInt(0)
		}
	}

	// get usage for the current billing period
	currentUsage, err := l.CronusV1.GetUsage(ctx, projectUUID, false)
	if err != nil {
		return liquid.ServiceUsageReport{}, err
	}
	logg.Debug("currentUsage = %#v", currentUsage)

	// if a new billing period has started, add the previous billing period's
	// final tally into `state.PreviousTotals`
	var newSerializedState json.RawMessage
	if state.CurrentPeriod.StartDate == currentUsage.StartDate {
		newSerializedState = req.SerializedState
	} else {
		prevUsage, err := l.CronusV1.GetUsage(ctx, projectUUID, true)
		if err != nil {
			return liquid.ServiceUsageReport{}, err
		}
		logg.Debug("prevUsage = %#v", prevUsage)
		if state.CurrentPeriod.StartDate != prevUsage.StartDate && state.CurrentPeriod.StartDate != "1970-01-01" {
			return liquid.ServiceUsageReport{}, fmt.Errorf(
				"cannot start new billing period: expected previous billing period to end by %s, but actually ended %s",
				state.CurrentPeriod.StartDate, prevUsage.StartDate,
			)
		}

		state.PreviousTotals.AttachmentsSize = bigintPlusUint64(state.PreviousTotals.AttachmentsSize, prevUsage.AttachmentsSize)
		state.PreviousTotals.DataTransferIn = bigintPlusUint64(state.PreviousTotals.DataTransferIn, prevUsage.DataTransferIn)
		state.PreviousTotals.DataTransferOut = bigintPlusUint64(state.PreviousTotals.DataTransferOut, prevUsage.DataTransferOut)
		state.PreviousTotals.Recipients = bigintPlusUint64(state.PreviousTotals.Recipients, prevUsage.Recipients)
		state.PreviousTotals.DataSentAWS = bigintPlusUint64(state.PreviousTotals.DataSentAWS, prevUsage.DataSentAWS)
		state.PreviousTotals.DataReceivedAWS = bigintPlusUint64(state.PreviousTotals.DataReceivedAWS, prevUsage.DataReceivedAWS)
		state.PreviousTotals.MessagesSentAWS = bigintPlusUint64(state.PreviousTotals.MessagesSentAWS, prevUsage.MessagesSentAWS)
		state.PreviousTotals.MessagesReceivedAWS = bigintPlusUint64(state.PreviousTotals.MessagesReceivedAWS, prevUsage.MessagesReceivedAWS)
		state.PreviousTotals.DataSentPostfix = bigintPlusUint64(state.PreviousTotals.DataSentPostfix, prevUsage.DataSentPostfix)
		state.PreviousTotals.DataReceivedPostfix = bigintPlusUint64(state.PreviousTotals.DataReceivedPostfix, prevUsage.DataReceivedPostfix)
		state.PreviousTotals.MessagesSentPostfix = bigintPlusUint64(state.PreviousTotals.MessagesSentPostfix, prevUsage.MessagesSentPostfix)
		state.PreviousTotals.MessagesReceivedPostfix = bigintPlusUint64(state.PreviousTotals.MessagesReceivedPostfix, prevUsage.MessagesReceivedPostfix)
		state.CurrentPeriod.StartDate = currentUsage.StartDate
		state.Version = Some(versionV1)

		newSerializedStateBytes, err := json.Marshal(state)
		if err != nil {
			return liquid.ServiceUsageReport{}, fmt.Errorf("cannot serialize new state: %w", err)
		}
		newSerializedState = json.RawMessage(newSerializedStateBytes)
	}

	// obtain the current running totals by adding the current billing period's
	// running tally to the previous totals
	buildRateReport := func(previous *big.Int, current uint64) *liquid.RateUsageReport {
		return &liquid.RateUsageReport{
			PerAZ: liquid.InAnyAZ(liquid.AZRateUsageReport{
				Usage: Some(bigintPlusUint64(previous, current)),
			}),
		}
	}
	return liquid.ServiceUsageReport{
		InfoVersion: serviceInfo.Version,
		Rates: map[liquid.RateName]*liquid.RateUsageReport{
			"attachment_size":           buildRateReport(state.PreviousTotals.AttachmentsSize, currentUsage.AttachmentsSize),
			"data_transfer_in":          buildRateReport(state.PreviousTotals.DataTransferIn, currentUsage.DataTransferIn),
			"data_transfer_out":         buildRateReport(state.PreviousTotals.DataTransferOut, currentUsage.DataTransferOut),
			"recipients":                buildRateReport(state.PreviousTotals.Recipients, currentUsage.Recipients),
			"messages_sent_aws":         buildRateReport(state.PreviousTotals.MessagesSentAWS, currentUsage.MessagesSentAWS),
			"messages_received_aws":     buildRateReport(state.PreviousTotals.MessagesReceivedAWS, currentUsage.MessagesReceivedAWS),
			"data_sent_aws":             buildRateReport(state.PreviousTotals.DataSentAWS, currentUsage.DataSentAWS),
			"data_received_aws":         buildRateReport(state.PreviousTotals.DataReceivedAWS, currentUsage.DataReceivedAWS),
			"messages_sent_postfix":     buildRateReport(state.PreviousTotals.MessagesSentPostfix, currentUsage.MessagesSentPostfix),
			"messages_received_postfix": buildRateReport(state.PreviousTotals.MessagesReceivedPostfix, currentUsage.MessagesReceivedPostfix),
			"data_sent_postfix":         buildRateReport(state.PreviousTotals.DataSentPostfix, currentUsage.DataSentPostfix),
			"data_received_postfix":     buildRateReport(state.PreviousTotals.DataReceivedPostfix, currentUsage.DataReceivedPostfix),
		},
		SerializedState: newSerializedState,
	}, nil
}

func bigintPlusUint64(a *big.Int, u uint64) *big.Int {
	var b big.Int
	b.SetUint64(u)
	var c big.Int
	return c.Add(a, &b)
}

// SetQuota implements the liquidapi.Logic interface.
func (l *Logic) SetQuota(ctx context.Context, projectUUID string, req liquid.ServiceQuotaRequest, serviceInfo liquid.ServiceInfo) error {
	// no resources with quota exist here
	return nil
}

// ReviewCommitmentChange implements the liquidapi.Logic interface.
func (l *Logic) ReviewCommitmentChange(ctx context.Context, req liquid.CommitmentChangeRequest, serviceInfo liquid.ServiceInfo) (liquid.CommitmentChangeResponse, error) {
	err := errors.New("this liquid does not manage commitments")
	return liquid.CommitmentChangeResponse{}, respondwith.CustomStatus(http.StatusBadRequest, err)
}
