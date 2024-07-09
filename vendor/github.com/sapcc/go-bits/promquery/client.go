/*******************************************************************************
*
* Copyright 2022 SAP SE
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

// Package promquery provides a simplified interface for executing Prometheus
// queries. This interface is suitable for usecases where applications only
// need to collect single values or sets of values without additional label
// information.
package promquery

import (
	"context"
	"fmt"
	"time"

	prom_v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"

	"github.com/sapcc/go-bits/logg"
)

// Client provides API access to a Prometheus server. It is constructed through
// the Connect method on type Config.
type Client struct {
	api prom_v1.API
}

// GetVector executes a Prometheus query and returns a vector of results.
func (c Client) GetVector(ctx context.Context, queryStr string) (model.Vector, error) {
	value, warnings, err := c.api.Query(ctx, queryStr, time.Now())
	if err != nil {
		return nil, fmt.Errorf("could not execute Prometheus query: %s: %w", queryStr, err)
	}
	for _, warning := range warnings {
		logg.Info("Prometheus query produced warning: %s", warning)
	}

	resultVector, ok := value.(model.Vector)
	if !ok {
		return nil, fmt.Errorf("could not execute Prometheus query: %s: unexpected type %T", queryStr, value)
	}
	return resultVector, nil
}

// GetSingleValue executes a Prometheus query and returns the result value. If
// the query produces multiple values, only the first value will be returned.
//
// If the query produces no values, the `defaultValue` will be returned if one
// was supplied. Otherwise, the returned error will be of type NoRowsError.
// That condition can be checked with `promquery.IsErrNoRows(err)`.
func (c Client) GetSingleValue(ctx context.Context, queryStr string, defaultValue *float64) (float64, error) {
	resultVector, err := c.GetVector(ctx, queryStr)
	if err != nil {
		return 0, err
	}

	switch resultVector.Len() {
	case 0:
		if defaultValue != nil {
			return *defaultValue, nil
		}
		return 0, NoRowsError{Query: queryStr}
	case 1:
		return float64(resultVector[0].Value), nil
	default:
		// suppress the log message when all values are the same (this can happen
		// when an adventurous Prometheus configuration causes the NetApp exporter
		// to be scraped twice)
		firstValue := resultVector[0].Value
		allTheSame := true
		for _, entry := range resultVector {
			if firstValue != entry.Value {
				allTheSame = false
				break
			}
		}
		if !allTheSame {
			logg.Info("Prometheus query returned multiple results (only the first value will be used): %s", queryStr)
		}
		return float64(resultVector[0].Value), nil
	}
}

// API returns the underlying API client from the Prometheus library. This
// should only be used if the simplified APIs in this package do not suffice.
func (c Client) API() prom_v1.API {
	return c.api
}
