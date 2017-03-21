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
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sapcc/limes/pkg/collector"
	"github.com/sapcc/limes/pkg/db"
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
	cluster, exists := config.Clusters[os.Args[2]]
	if !exists {
		util.LogFatal("no such cluster configured: " + os.Args[2])
	}
	driver, err := limes.NewDriver(cluster)
	if err != nil {
		util.LogFatal(err.Error())
	}

	//start collector threads (NOTE: Many people use a pair of sync.WaitGroup and
	//stop channel to shutdown threads in a controlled manner. I decided against
	//that for now, and instead construct worker threads in such a way that they
	//can be terminated at any time without leaving the system in an inconsistent
	//state, mostly through usage of DB transactions.)
	for _, service := range cluster.Services {
		plugin := limes.GetPlugin(service.Type)
		if plugin == nil {
			util.LogError("skipping service %s: no suitable collector plugin found", service.Type)
			continue
		}
		c := collector.NewCollector(driver, plugin)
		go c.Scrape()
		go c.ScanCapacity()
	}

	//start those collector threads which operate over all services simultaneously
	c := collector.NewCollector(driver, nil)
	go c.CheckConsistency()
	go func() {
		for {
			_, err := collector.ScanDomains(driver, collector.ScanDomainsOpts{ScanAllProjects: true})
			if err != nil {
				util.LogError(err.Error())
			}
			time.Sleep(discoverInterval)
		}
	}()

	//use main thread to emit Prometheus metrics
	http.Handle("/metrics", promhttp.Handler())
	util.LogFatal(http.ListenAndServe(config.Collector.MetricsListenAddress, nil).Error())
}
