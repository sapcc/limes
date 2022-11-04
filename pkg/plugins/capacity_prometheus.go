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
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/gophercloud/gophercloud"
	prom_api "github.com/prometheus/client_golang/api"
	prom_v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/model"
	"github.com/sapcc/go-bits/logg"
	"golang.org/x/net/context"

	"github.com/sapcc/limes/pkg/core"
)

type capacityPrometheusPlugin struct {
	cfg core.CapacitorConfiguration
}

func init() {
	core.CapacityPluginRegistry.Add(func() core.CapacityPlugin { return &capacityPrometheusPlugin{} })
}

func prometheusClient(cfg core.PrometheusAPIConfiguration) (prom_v1.API, error) {
	if cfg.URL == "" {
		return nil, errors.New("missing configuration parameter: url")
	}

	roundTripper := prom_api.DefaultRoundTripper

	tlsConfig := &tls.Config{} //nolint:gosec // used for a client which defaults to TLS version 1.2
	//If one of the following is set, so must be the other one
	if cfg.ClientCertificatePath != "" || cfg.ClientCertificateKeyPath != "" {
		if cfg.ClientCertificatePath == "" {
			return nil, errors.New("missing configuration parameter: cert")
		}
		if cfg.ClientCertificateKeyPath == "" {
			return nil, errors.New("missing configuration parameter: key")
		}

		clientCert, err := tls.LoadX509KeyPair(cfg.ClientCertificatePath, cfg.ClientCertificateKeyPath)
		if err != nil {
			return nil, err
		}
		tlsConfig.Certificates = []tls.Certificate{clientCert}
	}
	if cfg.ServerCACertificatePath != "" {
		serverCACert, err := os.ReadFile(cfg.ServerCACertificatePath)
		if err != nil {
			return nil, fmt.Errorf("cannot load CA certificate from %s: %s", cfg.ServerCACertificatePath, err.Error())
		}

		certPool := x509.NewCertPool()
		certPool.AppendCertsFromPEM(serverCACert)
		tlsConfig.RootCAs = certPool
	}

	if transport, ok := roundTripper.(*http.Transport); ok {
		transport.TLSClientConfig = tlsConfig
	} else {
		return nil, fmt.Errorf("expected roundTripper of type \"*http.Transport\", got %T", roundTripper)
	}

	client, err := prom_api.NewClient(prom_api.Config{Address: cfg.URL, RoundTripper: roundTripper})
	if err != nil {
		return nil, fmt.Errorf("cannot connect to Prometheus at %s: %s", cfg.URL, err.Error())
	}
	return prom_v1.NewAPI(client), nil
}

func prometheusGetSingleValue(client prom_v1.API, queryStr string, defaultValue *float64) (float64, error) {
	value, warnings, err := client.Query(context.Background(), queryStr, time.Now())
	for _, warning := range warnings {
		logg.Info("Prometheus query produced warning: %s", warning)
	}
	if err != nil {
		//nolint:stylecheck //Prometheus is a proper name
		return 0, fmt.Errorf("Prometheus query failed: %s: %s", queryStr, err.Error())
	}
	resultVector, ok := value.(model.Vector)
	if !ok {
		//nolint:stylecheck //Prometheus is a proper name
		return 0, fmt.Errorf("Prometheus query failed: %s: unexpected type %T", queryStr, value)
	}

	switch resultVector.Len() {
	case 0:
		if defaultValue != nil {
			return *defaultValue, nil
		}
		//nolint:stylecheck //Prometheus is a proper name
		return 0, fmt.Errorf("Prometheus query returned empty result: %s", queryStr)
	case 1:
		return float64(resultVector[0].Value), nil
	default:
		logg.Info("Prometheus query returned more than one result: %s (only the first value will be used)", queryStr)
		return float64(resultVector[0].Value), nil
	}
}

// Init implements the core.CapacityPlugin interface.
func (p *capacityPrometheusPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, c core.CapacitorConfiguration, scrapeSubcapacities map[string]map[string]bool) error {
	p.cfg = c
	return nil
}

// PluginTypeID implements the core.CapacityPlugin interface.
func (p *capacityPrometheusPlugin) PluginTypeID() string {
	return "prometheus"
}

// Scrape implements the core.CapacityPlugin interface.
func (p *capacityPrometheusPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (result map[string]map[string]core.CapacityData, _ string, err error) {
	client, err := prometheusClient(p.cfg.Prometheus.APIConfig)
	if err != nil {
		return nil, "", err
	}

	result = make(map[string]map[string]core.CapacityData)
	for serviceType, queries := range p.cfg.Prometheus.Queries {
		serviceResult := make(map[string]core.CapacityData)
		for resourceName, query := range queries {
			value, err := prometheusGetSingleValue(client, query, nil)
			if err != nil {
				return nil, "", err
			}
			serviceResult[resourceName] = core.CapacityData{Capacity: uint64(value)}
		}
		result[serviceType] = serviceResult
	}
	return result, "", nil
}

// DescribeMetrics implements the core.CapacityPlugin interface.
func (p *capacityPrometheusPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	//not used by this plugin
}

// CollectMetrics implements the core.CapacityPlugin interface.
func (p *capacityPrometheusPlugin) CollectMetrics(ch chan<- prometheus.Metric, clusterID, serializedMetrics string) error {
	//not used by this plugin
	return nil
}
