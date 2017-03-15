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

package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	"github.com/sapcc/limes/pkg/limes"
)

//VersionData is used by version advertisement handlers.
type VersionData struct {
	Status string            `json:"status"`
	ID     string            `json:"id"`
	Links  []VersionLinkData `json:"links"`
}

//VersionLinkData is used by version advertisement handlers, as part of the
//VersionData struct.
type VersionLinkData struct {
	URL      string `json:"href"`
	Relation string `json:"rel"`
	Type     string `json:"type,omitempty"`
}

type v1Provider struct {
	Driver      limes.Driver
	Config      limes.APIConfiguration
	VersionData VersionData
}

//NewV1Router creates a mux.Router that serves the Limes v1 API.
func NewV1Router(driver limes.Driver, config limes.APIConfiguration) (*mux.Router, VersionData) {
	r := mux.NewRouter()
	p := &v1Provider{
		Driver: driver,
		Config: config,
	}
	p.VersionData = VersionData{
		Status: "EXPERIMENTAL",
		ID:     "v1",
		Links: []VersionLinkData{
			VersionLinkData{
				Relation: "self",
				URL:      p.Path(),
			},
			VersionLinkData{
				Relation: "describedby",
				URL:      "https://github.com/sapcc/limes/tree/master/docs",
				Type:     "text/html",
			},
		},
	}

	r.Methods("GET").Path("/v1/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ReturnJSON(w, 200, p.VersionData)
	})

	return r, p.VersionData
}

//ReturnJSON is a convenience function for HTTP handlers returning JSON data.
//The `code` argument specifies the HTTP response code, usually 200.
func ReturnJSON(w http.ResponseWriter, code int, data interface{}) {
	bytes, err := json.Marshal(&data)
	if err == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		w.Write(bytes)
	} else {
		http.Error(w, err.Error(), 500)
	}
}

//Path constructs a full URL for a given URL path below the /v1/ endpoint.
func (p *v1Provider) Path(elements ...string) string {
	parts := []string{
		strings.TrimSuffix(p.Driver.Cluster().CatalogURL, "/"),
		"v1",
	}
	parts = append(parts, elements...)
	return strings.Join(parts, "/")
}
