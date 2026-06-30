// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"cmp"
	"fmt"
	"net"
	"net/http"
	"net/url"

	"github.com/gorilla/mux"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/osext"
)

type versionProvider struct {
	DomainNames DomainNames
}

// NewVersionProviderAPI creates an httpapi.API that serves the version advertisement API.
func NewVersionProviderAPI(domainNames DomainNames) httpapi.API {
	return &versionProvider{
		DomainNames: domainNames,
	}
}

// AddTo implements the httpapi.API interface.
func (p *versionProvider) AddTo(r *mux.Router) {
	// NOTE: The intent of this is to provide a minimal response on every URL
	//       that appears in the Keystone catalog. These are, by service type:
	//
	// - resources:         https://$LIMES_API_DOMAIN_NAME_V1/
	// - limitas-resources: https://$LIMES_API_DOMAIN_NAME_V2/resources/v2/
	// - limitas-rates:     https://$LIMES_API_DOMAIN_NAME_V2/rates/v2/
	//
	// Also, for maximum backwards compatibility, https://$LIMES_API_DOMAIN_NAME_V1/v1/ is also understood.

	enforceV1 := EnforceDomainName(p.DomainNames.V1)
	enforceV2 := EnforceDomainName(p.DomainNames.V2)

	for _, v1Path := range []string{"/", "/v1/"} {
		r.Methods("HEAD", "GET").Path(v1Path).Handler(enforceV1(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			httpapi.IdentifyEndpoint(r, v1Path)
			http.Redirect(w, r, "https://github.com/sapcc/limes/blob/master/docs/users/api-v1-specification.md", http.StatusSeeOther)
		})))
	}
	r.Methods("HEAD", "GET").Path("/resources/v2/").Handler(enforceV2(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpapi.IdentifyEndpoint(r, "/resources/v2/")
		// TODO: replace with link to pkg.go.dev of the resource API spec
		http.Redirect(w, r, "https://github.com/sapcc/limes/blob/master/docs/users/api-v2-specification.md", http.StatusSeeOther)
	})))
	r.Methods("HEAD", "GET").Path("/rates/v2/").Handler(enforceV2(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpapi.IdentifyEndpoint(r, "/rates/v2/")
		// TODO: replace with link to pkg.go.dev of the rate API spec
		http.Redirect(w, r, "https://github.com/sapcc/limes/blob/master/docs/users/api-v2-specification.md", http.StatusSeeOther)
	})))
}

// DomainNames holds the domain names used by Limes's APIs.
type DomainNames struct {
	V1 string `json:"v1"`
	V2 string `json:"v2"`
}

// CollectDomainNamesFromEnv constructs a [DomainNames] instance.
func CollectDomainNamesFromEnv() (result DomainNames, err error) {
	getAndCheck := func(key string) (string, error) {
		value, err := osext.NeedGetenv(key)
		if err != nil {
			return "", err
		}
		// check that `value` is a hostname and nothing else
		testURL, err := url.Parse("https://" + value)
		if err != nil || testURL.Host != value {
			return "", fmt.Errorf("invalid value for %s: expected a hostname, but got %q", key, value)
		}
		return value, nil
	}

	result.V1, err = getAndCheck("LIMES_API_DOMAIN_NAME_V1")
	if err != nil {
		return
	}
	result.V2, err = getAndCheck("LIMES_API_DOMAIN_NAME_V2")
	return
}

// EnforceDomainName returns a middleware that rejects requests where
// the hostname in the request URL does not match the given value.
func EnforceDomainName(domainName string) mux.MiddlewareFunc {
	return func(inner http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hostName := getHostNameFor(r)
			if hostName == domainName {
				inner.ServeHTTP(w, r)
			} else {
				msg := fmt.Sprintf("endpoint %s cannot be accessed on %s", r.URL.EscapedPath(), hostName)
				http.Error(w, msg, http.StatusBadRequest)
			}
		})
	}
}

func getHostNameFor(r *http.Request) string {
	hostAndMaybePort := cmp.Or(r.Header.Get("X-Forwarded-Host"), r.Host)
	host, _, err := net.SplitHostPort(hostAndMaybePort)
	if err == nil {
		return host
	} else {
		return hostAndMaybePort // no port to split off
	}
}
