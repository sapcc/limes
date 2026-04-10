// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/gorilla/mux"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/respondwith"

	"github.com/sapcc/limes/internal/core"
)

type versionProvider struct {
	Cluster                *core.Cluster
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
func NewVersionProviderAPI(cluster *core.Cluster) httpapi.API {
	return &versionProvider{
		Cluster:              cluster,
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

// Path constructs a full URL for a given URL path below the respective /v[version]/ endpoint.
func (p *versionProvider) Path(version string, elements ...string) string {
	parts := []string{strings.TrimSuffix(p.Cluster.Config.CatalogURL, "/"), version}
	parts = append(parts, elements...)
	return strings.Join(parts, "/") + "/"
}
