// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"net/http"
	"time"

	"github.com/sapcc/go-bits/logg"
)

// AddLoggingRoundTripper adds logging for long round trips to http.RoundTripper.
// This is used to provide visibility into slow backend API calls.
func AddLoggingRoundTripper(inner http.RoundTripper) http.RoundTripper {
	return loggingRoundTripper{inner}
}

type loggingRoundTripper struct {
	Inner http.RoundTripper
}

// RoundTrip implements the http.RoundTripper interface.
func (rt loggingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := rt.Inner.RoundTrip(req)
	duration := time.Since(start)

	if err == nil && duration > 1*time.Minute {
		logg.Info("API call has taken excessively long (%s): %s", duration.String(), req.URL.String())
	}

	return resp, err
}
