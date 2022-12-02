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
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	prom_api "github.com/prometheus/client_golang/api"
	prom_v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	"github.com/sapcc/go-bits/logg"
)

// PrometheusAPIConfiguration contains configuration parameters for a Prometheus API.
// Only the URL field is required in the format: "http<s>://localhost<:9090>" (port is optional).
type PrometheusAPIConfiguration struct {
	URL                      string `yaml:"url"`
	ClientCertificatePath    string `yaml:"cert"`
	ClientCertificateKeyPath string `yaml:"key"`
	ServerCACertificatePath  string `yaml:"ca_cert"`
}

func prometheusClient(cfg PrometheusAPIConfiguration) (prom_v1.API, error) {
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