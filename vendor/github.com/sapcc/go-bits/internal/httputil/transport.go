// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package httputil

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

// TransportOpts contains options for building a *http.Transport object.
type TransportOpts struct {
	ServerCACertificatePath  string
	ClientCertificatePath    string
	ClientCertificateKeyPath string
}

// NewTransport builds an *http.Transport based on the provided options.
// If no special options are set, this will return an instance
// that is functionally equivalent to the default settings of http.Defaultt.
func NewTransport(opts TransportOpts) (*http.Transport, error) {
	if opts.ClientCertificatePath == "" && opts.ClientCertificateKeyPath != "" {
		return nil, errors.New("private key given, but no client certificate given")
	}
	if opts.ClientCertificatePath != "" && opts.ClientCertificateKeyPath == "" {
		return nil, errors.New("client certificate given, but no private key given")
	}

	// This is intended to construct `result` in the same way as net/http.DefaultTransport.
	// If TestDefaultTransport fails, update this paragraph to match the initialization of that variable in std.
	//
	// NOTE: We are not just using http.DefaultTransport.Clone() because:
	//       1) http.DefaultTransport is an http.RoundTripper and may contain a type other than *http.Transport
	//       2) http.Transport.Clone() has known bugs, see <https://github.com/golang/go/issues/39302>
	result := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	if opts.ClientCertificatePath != "" || opts.ServerCACertificatePath != "" {
		// only instantiate TLSClientConfig when actually necessary; its presence may disable
		// useful behaviors like HTTP/2-by-default, so it should only be present when necessary
		result.TLSClientConfig = &tls.Config{}
	}

	if opts.ClientCertificatePath != "" {
		clientCert, err := tls.LoadX509KeyPair(opts.ClientCertificatePath, opts.ClientCertificateKeyPath)
		if err != nil {
			return nil, fmt.Errorf("cannot load client certificate from %s and %s: %w",
				opts.ClientCertificatePath, opts.ClientCertificateKeyPath, err)
		}
		result.TLSClientConfig.Certificates = []tls.Certificate{clientCert}
	}

	if opts.ServerCACertificatePath != "" {
		serverCACert, err := os.ReadFile(opts.ServerCACertificatePath)
		if err != nil {
			return nil, fmt.Errorf("cannot load CA certificate from %s: %w",
				opts.ServerCACertificatePath, err)
		}
		certPool := x509.NewCertPool()
		certPool.AppendCertsFromPEM(serverCACert)
		result.TLSClientConfig.RootCAs = certPool
	}

	return result, nil
}
