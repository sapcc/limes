// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package promquery

import (
	"errors"
	"fmt"
	"os"

	prom_api "github.com/prometheus/client_golang/api"
	prom_v1 "github.com/prometheus/client_golang/api/prometheus/v1"

	"github.com/sapcc/go-bits/httpext"
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

	transport, err := httpext.NewTransport(httpext.TransportOpts{
		ServerCACertificatePath:  cfg.ServerCACertificatePath,
		ClientCertificatePath:    cfg.ClientCertificatePath,
		ClientCertificateKeyPath: cfg.ClientCertificateKeyPath,
	})
	if err != nil {
		return Client{}, fmt.Errorf("cannot connect to Prometheus at %s: %w", cfg.ServerURL, err)
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
