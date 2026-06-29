// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"cmp"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"

	"github.com/gorilla/mux"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/osext"
	"github.com/sapcc/go-bits/respondwith"

	"github.com/sapcc/limes/internal/core"
)

type versionProvider struct {
	Cluster                *core.Cluster
	DomainNames            DomainNames
	currentVersion         string
	experimentalVersions   []string
	otherSupportedVersions []string
}

// VersionData is used by version advertisement API.
type VersionData struct {
	Status string            `json:"status"`
	ID     string            `json:"id"`
	Links  []VersionLinkData `json:"links"`
}

// VersionLinkData is used by version advertisement API, as part of the
// VersionData struct.
type VersionLinkData struct {
	URL      string `json:"href"`
	Relation string `json:"rel"`
	Type     string `json:"type,omitempty"`
}

// NewVersionProviderAPI creates an httpapi.API that serves the version advertisement API.
func NewVersionProviderAPI(cluster *core.Cluster, domainNames DomainNames) httpapi.API {
	return &versionProvider{
		Cluster:              cluster,
		DomainNames:          domainNames,
		currentVersion:       "v1",
		experimentalVersions: []string{"v2"},
	}
}

func (p *versionProvider) generateVersionData(version string) VersionData {
	status := "SUPPORTED"
	if version == p.currentVersion {
		status = "CURRENT"
	} else if slices.Contains(p.experimentalVersions, version) {
		status = "EXPERIMENTAL"
	}
	return VersionData{
		Status: status,
		ID:     version,
		Links: []VersionLinkData{
			{
				Relation: "self",
				URL:      p.Path(version),
			},
			{
				Relation: "describedby",
				URL:      fmt.Sprintf("https://github.com/sapcc/limes/blob/master/docs/users/api-%s-specification.md", version),
				Type:     "text/html",
			},
		},
	}
}

// AddTo implements the httpapi.API interface.
func (p *versionProvider) AddTo(r *mux.Router) {
	allVersions := []string{p.currentVersion}
	allVersions = append(allVersions, p.experimentalVersions...)
	allVersions = append(allVersions, p.otherSupportedVersions...)
	allVersionData := make([]VersionData, len(allVersions))
	for i, version := range allVersions {
		allVersionData[i] = p.generateVersionData(version)
	}

	r.Methods("HEAD", "GET").Path("/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpapi.IdentifyEndpoint(r, "/")
		httpapi.SkipRequestLog(r)
		respondwith.JSON(w, 300, map[string]any{"versions": allVersionData})
	})

	for i, versionData := range allVersionData {
		version := allVersions[i]
		r.Methods("GET").Path("/" + version + "/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			httpapi.IdentifyEndpoint(r, "/"+version+"/")
			httpapi.SkipRequestLog(r)
			respondwith.JSON(w, 200, map[string]any{"version": versionData})
		})
	}
}

// Path constructs a full URL for the respective /v[version]/ endpoint.
func (p *versionProvider) Path(version string) string {
	u := url.URL{
		Scheme: "https",
		Host:   p.DomainNames.V2,
		Path:   version + "/",
	}
	if version == "v1" {
		u.Host = p.DomainNames.V1
	}
	return u.String()
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
