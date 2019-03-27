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
	"fmt"
	"time"

	"github.com/gophercloud/gophercloud"
	"github.com/prometheus/client_golang/api/prometheus"
	"github.com/prometheus/common/model"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/limes/pkg/core"
	"golang.org/x/net/context"
)

type capacityPrometheusPlugin struct {
	cfg core.CapacitorConfiguration
}

func init() {
	core.RegisterCapacityPlugin(func(c core.CapacitorConfiguration, scrapeSubcapacities map[string]map[string]bool) core.CapacityPlugin {
		return &capacityPrometheusPlugin{c}
	})
}

//Requires the url to prometheus à la "http<s>://localhost<:9090>",
//in our case even without port.
func prometheusClient(apiURL string) (prometheus.QueryAPI, error) {
	//default value
	if apiURL == "" {
		apiURL = "https://localhost:9090"
	}

	config := prometheus.Config{
		Address:   apiURL,
		Transport: prometheus.DefaultTransport,
	}
	client, err := prometheus.New(config)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to Prometheus at %s: %s", apiURL, err.Error())
	}
	return prometheus.NewQueryAPI(client), nil
}

func prometheusGetSingleValue(client prometheus.QueryAPI, queryStr string) (float64, error) {
	value, err := client.Query(context.Background(), queryStr, time.Now())
	if err != nil {
		return 0, fmt.Errorf("Prometheus query failed: %s: %s", queryStr, err.Error())
	}
	resultVector, ok := value.(model.Vector)
	if !ok {
		return 0, fmt.Errorf("Prometheus query failed: %s: unexpected type %T", queryStr, value)
	}

	switch resultVector.Len() {
	case 0:
		return 0, fmt.Errorf("Prometheus query returned empty result: %s", queryStr)
	default:
		logg.Info("Prometheus query returned more than one result: %s (only the first value will be used)", queryStr)
		fallthrough
	case 1:
		return float64(resultVector[0].Value), nil
	}
}

//ID implements the core.CapacityPlugin interface.
func (p *capacityPrometheusPlugin) ID() string {
	return "prometheus"
}

//Scrape implements the core.CapacityPlugin interface.
func (p *capacityPrometheusPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID string) (map[string]map[string]core.CapacityData, error) {

	client, err := prometheusClient(p.cfg.Prometheus.APIURL)
	if err != nil {
		return nil, err
	}

	result := make(map[string]map[string]core.CapacityData)
	for serviceType, queries := range p.cfg.Prometheus.Queries {
		serviceResult := make(map[string]core.CapacityData)
		for resourceName, query := range queries {
			value, err := prometheusGetSingleValue(client, query)
			if err != nil {
				return nil, err
			}
			serviceResult[resourceName] = core.CapacityData{Capacity: uint64(value)}
		}
		result[serviceType] = serviceResult
	}
	return result, nil
}
