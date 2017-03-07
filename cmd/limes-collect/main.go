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
	"os"
	"time"

	"github.com/sapcc/limes/pkg/collector"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/drivers"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/util"

	_ "github.com/sapcc/limes/pkg/plugins"
)

var discoverInterval = 3 * time.Minute

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
	cluster, err := limes.NewCluster(config, os.Args[2])
	if err != nil {
		util.LogFatal(err.Error())
	}
	driver := drivers.NewDriver(cluster)

	//start scraper threads (NOTE: Many people use a pair of sync.WaitGroup and
	//stop channel to shutdown threads in a controlled manner. I decided against
	//that for now, and instead construct worker threads in such a way that they
	//can be terminated at any time without leaving the system in an inconsistent
	//state, mostly through usage of DB transactions.)
	for _, service := range cluster.EnabledServices() {
		go collector.Scrape(driver, service.Type)
	}
	go collector.ScanCapacity(driver)

	//since we don't have to manage thread lifetime in the main thread, I use it to check Keystone regularly
	//TODO: before starting this, walk over the existing project_services once to
	//ensure that there is an entry for each project and service (this might not
	//be the case if new services were added to the cluster configuration)
	for {
		_, err := collector.ScanDomains(driver, collector.ScanDomainsOpts{ScanAllProjects: true})
		if err != nil {
			util.LogError(err.Error())
		}
		time.Sleep(discoverInterval)
	}
}
