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

package main

import (
	"fmt"
	"net/http"
	"os"

	"github.com/sapcc/limes/pkg/api"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/util"

	_ "github.com/sapcc/limes/pkg/plugins"
)

func main() {
	//expect two arguments (config file name and cluster ID)
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "Usage: %s <config-file> <cluster-id>\n", os.Args[0])
		os.Exit(1)
	}
	config := limes.NewConfiguration(os.Args[1])

	//connect to database
	err := db.Init(config.Database)
	if err != nil {
		util.LogFatal(err.Error())
	}

	//connect to cluster
	cluster, exists := config.Clusters[os.Args[2]]
	if !exists {
		util.LogFatal("no such cluster configured: " + os.Args[2])
	}
	driver, err := limes.NewDriver(cluster)
	if err != nil {
		util.LogFatal(err.Error())
	}

	//hook up the v1 API (this code is structured so that a newer API version can
	//be added easily later)
	v1Router, v1VersionData := api.NewV1Router(driver, config)
	http.Handle("/v1/", v1Router)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		allVersions := struct {
			Versions []api.VersionData `json:"versions"`
		}{[]api.VersionData{v1VersionData}}
		api.ReturnJSON(w, 300, allVersions)
	})

	//start HTTP server
	util.LogFatal(http.ListenAndServe(config.API.ListenAddress, nil).Error())
}
