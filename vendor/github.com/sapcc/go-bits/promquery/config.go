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

package promquery

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	prom_api "github.com/prometheus/client_golang/api"
	prom_v1 "github.com/prometheus/client_golang/api/prometheus/v1"

	"github.com/sapcc/go-bits/osext"
)

// Config contains the set of configuration parameters that are required for
// establishing a connection to a Prometheus instance's API.
type Config struct {
	// Required: Main URL of Prometheus instance.
	ServerURL string `json:"url" yaml:"url"`
	// Optional: To check validity of TLS server certificate.
	ServerCACertificatePath string `json:"ca_cert" yaml:"ca_cert"`
	// Optional: TLS client certificate to present while connecting.
	ClientCertificatePath string `json:"cert" yaml:"cert"`
	// Required if ClientCertificatePath is given: Private key for TLS client certificate.
	ClientCertificateKeyPath string `json:"key" yaml:"key"`

	// Cache for repeated calls to Connect().
	cachedConnection prom_v1.API `json:"-" yaml:"-"`
}

// ConfigFromEnv fills a Config object from the following environment variables:
//
//	${envPrefix}_URL    - required
//	${envPrefix}_CACERT - optional
//	${envPrefix}_CERT   - optional
//	${envPrefix}_KEY    - optional (required if client cert is given)
//
// This function exits through logg.Fatal() if a required value is missing.
func ConfigFromEnv(envPrefix string) Config {
	cfg := Config{
		ServerURL:               osext.MustGetenv(envPrefix + "_URL"),
		ServerCACertificatePath: os.Getenv(envPrefix + "_CACERT"),
		ClientCertificatePath:   os.Getenv(envPrefix + "_CERT"),
	}
	if cfg.ClientCertificatePath != "" {
		cfg.ClientCertificateKeyPath = osext.MustGetenv(envPrefix + "_KEY")
	}
	return cfg
}

// Connect sets up a Prometheus client from the given Config.
func (cfg Config) Connect() (Client, error) {
	if cfg.cachedConnection != nil {
		return Client{cfg.cachedConnection}, nil
	}

	if cfg.ServerURL == "" {
		return Client{}, errors.New("cannot connect to Prometheus: missing server URL")
	}
	if cfg.ClientCertificatePath == "" && cfg.ClientCertificateKeyPath != "" {
		return Client{}, fmt.Errorf("cannot connect to Prometheus at %s: private key given, but no client certificate given", cfg.ServerURL)
	}
	if cfg.ClientCertificatePath != "" && cfg.ClientCertificateKeyPath == "" {
		return Client{}, fmt.Errorf("cannot connect to Prometheus at %s: client certificate given, but no private key given", cfg.ServerURL)
	}

	// same configuration as prom_api.DefaultRoundTripper (but we cannot just clone it because it contains a Mutex)
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	transport.TLSClientConfig = &tls.Config{} //nolint:gosec // used for a client which defaults to TLS version 1.2

	if cfg.ClientCertificatePath != "" {
		clientCert, err := tls.LoadX509KeyPair(cfg.ClientCertificatePath, cfg.ClientCertificateKeyPath)
		if err != nil {
			return Client{}, fmt.Errorf("cannot load client certificate from %s and %s: %w",
				cfg.ClientCertificatePath, cfg.ClientCertificateKeyPath, err)
		}
		transport.TLSClientConfig.Certificates = []tls.Certificate{clientCert}
	}

	if cfg.ServerCACertificatePath != "" {
		serverCACert, err := os.ReadFile(cfg.ServerCACertificatePath)
		if err != nil {
			return Client{}, fmt.Errorf("cannot load CA certificate from %s: %w",
				cfg.ServerCACertificatePath, err)
		}
		certPool := x509.NewCertPool()
		certPool.AppendCertsFromPEM(serverCACert)
		transport.TLSClientConfig.RootCAs = certPool
	}

	promCfg := prom_api.Config{
		Address:      cfg.ServerURL,
		RoundTripper: transport,
	}
	client, err := prom_api.NewClient(promCfg)
	if err != nil {
		return Client{}, fmt.Errorf("cannot connect to Prometheus at %s: %w", cfg.ServerURL, err)
	}

	cfg.cachedConnection = prom_v1.NewAPI(client) // speed up future calls to Connect()
	return Client{cfg.cachedConnection}, nil
}
