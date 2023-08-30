/*******************************************************************************
*
* Copyright 2023 SAP SE
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

//TODO: move this to go-bits/httpapi/pprofapi, as implied by the package comment

// Package pprofapi provides a httpapi.API wrapper for the net/http/pprof
// package. This is in a separate package and not the main httpapi package
// because importing net/http/pprof tampers with http.DefaultServeMux, so
// importing this package is only safe if the application does not use
// the http.DefaultServeMux instance.
package pprofapi

import (
	"net/http"
	"net/http/pprof"

	"github.com/gorilla/mux"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/httpext"
)

// API is a httpapi.API wrapping net/http/pprof. Unlike the default facility in
// net/http/pprof, the respective endpoints are only accessible to admin users.
type API struct {
	IsAuthorized func(r *http.Request) bool
}

// AddTo implements the httpapi.API interface.
func (a API) AddTo(r *mux.Router) {
	if a.IsAuthorized == nil {
		panic("API.AddTo() called with IsAuthorized == nil!")
	}

	a.attach(r, "/debug/pprof/", pprof.Index)
	a.attach(r, "/debug/pprof/cmdline", pprof.Cmdline)
	a.attach(r, "/debug/pprof/profile", pprof.Profile)
	a.attach(r, "/debug/pprof/symbol", pprof.Symbol)
	a.attach(r, "/debug/pprof/trace", pprof.Trace)
}

func (a API) attach(r *mux.Router, path string, inner http.HandlerFunc) {
	r.Methods("GET").Path(path).HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpapi.IdentifyEndpoint(r, path)
		httpapi.SkipRequestLog(r)
		if a.IsAuthorized(r) {
			inner(w, r)
		} else {
			http.Error(w, "forbidden", http.StatusForbidden)
		}
	})
}

// IsRequestFromLocalhost checks whether the given request originates from
// `127.0.0.1` or `::1`. It satisfies the interface of API.IsAuthorized.
func IsRequestFromLocalhost(r *http.Request) bool {
	ip := httpext.GetRequesterIPFor(r)
	return ip == "127.0.0.1" || ip == "::1"
}
