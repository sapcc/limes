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

package util

import (
	"crypto/tls"
	"net/http"
	"time"

	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/osext"
)

func init() {
	//I have some trouble getting Limes to connect to our staging OpenStack
	//through mitmproxy (which is very useful for development and debugging) when
	//TLS certificate verification is enabled. Therefore, allow to turn it off
	//with an env variable. (It's very important that this is not the standard
	//"DEBUG" variable. "DEBUG" is meant to be useful for production systems,
	//where you definitely don't want to turn off certificate verification.)
	if osext.GetenvBool("LIMES_INSECURE") {
		http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true,
		}
	}

	http.DefaultTransport = loggingRoundTripper{http.DefaultTransport}
}

// loggingRoundTripper adds logging for long round trips to http.RoundTripper.
// This is used to provide visibility into slow backend API calls.
type loggingRoundTripper struct {
	Inner http.RoundTripper
}

func (rt loggingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := rt.Inner.RoundTrip(req)
	duration := time.Since(start)

	if err == nil && duration > 1*time.Minute {
		logg.Info("API call has taken excessively long (%s): %s", duration.String(), req.URL.String())
	}

	return resp, err
}
