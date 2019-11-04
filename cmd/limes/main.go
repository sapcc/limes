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
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/cors"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/respondwith"
	"github.com/sapcc/limes/pkg/api"
	"github.com/sapcc/limes/pkg/collector"
	"github.com/sapcc/limes/pkg/core"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/util"

	_ "github.com/sapcc/limes/pkg/plugins"
)

var discoverInterval = 3 * time.Minute

func main() {
	//first two arguments must be task name and configuration file
	if len(os.Args) < 3 {
		printUsageAndExit()
	}
	taskName, configPath := os.Args[1], os.Args[2]

	//load configuration
	config := core.NewConfiguration(configPath)

	//all other tasks have the <cluster-id> as os.Args[3]
	if len(os.Args) < 4 {
		printUsageAndExit()
	}
	clusterID, remainingArgs := os.Args[3], os.Args[4:]

	//connect to database
	err := db.Init(config.Database)
	if err != nil {
		logg.Fatal(err.Error())
	}

	//connect to cluster
	cluster, exists := config.Clusters[clusterID]
	if !exists {
		logg.Fatal("no such cluster configured: " + clusterID)
	}
	err = cluster.Connect()
	if err != nil {
		logg.Fatal(util.ErrorToString(err))
	}

	//start audit trail
	AuditConfigPerCluster := make(map[string]core.CADFConfiguration)
	for _, cluster := range config.Clusters {
		AuditConfigPerCluster[cluster.ID] = cluster.Config.CADF
	}
	api.StartAuditTrail(AuditConfigPerCluster)

	//select task
	var task func(core.Configuration, *core.Cluster, []string) error
	switch taskName {
	case "collect":
		task = taskCollect
	case "serve":
		task = taskServe
	case "test-scrape":
		task = taskTestScrape
	case "test-scan-capacity":
		task = taskTestScanCapacity
	default:
		printUsageAndExit()
	}

	//run task
	err = task(config, cluster, remainingArgs)
	if err != nil {
		logg.Fatal(err.Error())
	}
}

var usageMessage = strings.Replace(strings.TrimSpace(`
Usage:
\t%s (collect|serve) <config-file> <cluster-id>
\t%s test-scrape <config-file> <cluster-id> <project-id>
\t%s test-scan-capacity <config-file> <cluster-id>
`), `\t`, "\t", -1) + "\n"

func printUsageAndExit() {
	fmt.Fprintln(os.Stderr, strings.Replace(usageMessage, "%s", os.Args[0], -1))
	os.Exit(1)
}

////////////////////////////////////////////////////////////////////////////////
// task: collect

func taskCollect(config core.Configuration, cluster *core.Cluster, args []string) error {
	if len(args) != 0 {
		printUsageAndExit()
	}

	//start scraping threads (NOTE: Many people use a pair of sync.WaitGroup and
	//stop channel to shutdown threads in a controlled manner. I decided against
	//that for now, and instead construct worker threads in such a way that they
	//can be terminated at any time without leaving the system in an inconsistent
	//state, mostly through usage of DB transactions.)
	for _, plugin := range cluster.QuotaPlugins {
		c := collector.NewCollector(cluster, plugin, config.Collector)
		go c.Scrape()
	}

	//start those collector threads which operate over all services simultaneously
	c := collector.NewCollector(cluster, nil, config.Collector)
	go c.CheckConsistency()
	go c.ScanCapacity()
	go func() {
		for {
			_, err := collector.ScanDomains(cluster, collector.ScanDomainsOpts{ScanAllProjects: true})
			if err != nil {
				logg.Error(util.ErrorToString(err))
			}
			time.Sleep(discoverInterval)
		}
	}()

	//use main thread to emit Prometheus metrics
	prometheus.MustRegister(&collector.AggregateMetricsCollector{
		Cluster: cluster,
	})
	if config.Collector.ExposeDataMetrics {
		prometheus.MustRegister(&collector.DataMetricsCollector{
			Cluster:      cluster,
			ReportZeroes: !config.Collector.SkipZeroForDataMetrics,
		})
	}
	http.Handle("/metrics", promhttp.Handler())
	logg.Info("listening on " + config.Collector.MetricsListenAddress)
	return http.ListenAndServe(config.Collector.MetricsListenAddress, nil)
}

////////////////////////////////////////////////////////////////////////////////
// task: serve

func taskServe(config core.Configuration, cluster *core.Cluster, args []string) error {
	if len(args) != 0 {
		printUsageAndExit()
	}

	//connect to *all* clusters - we may have to service cross-cluster requests
	for _, otherCluster := range config.Clusters {
		//Note that Connect() is idempotent, so this is safe even for `otherCluster == cluster`.
		err := otherCluster.Connect()
		if err != nil {
			logg.Fatal(util.ErrorToString(err))
		}
	}

	mainRouter := mux.NewRouter()

	//hook up the v1 API (this code is structured so that a newer API version can
	//be added easily later)
	v1Router, v1VersionData := api.NewV1Router(cluster, config)
	mainRouter.PathPrefix("/v1/").Handler(v1Router)

	//add the version advertisement that lists all available API versions
	mainRouter.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		allVersions := struct {
			Versions []api.VersionData `json:"versions"`
		}{[]api.VersionData{v1VersionData}}
		respondwith.JSON(w, 300, allVersions)
	})
	var handler http.Handler = mainRouter

	//add Prometheus instrumentation
	http.Handle("/metrics", promhttp.Handler())

	//add logging instrumentation
	handler = logg.Middleware{ExceptStatusCodes: config.API.RequestLog.ExceptStatusCodes}.Wrap(handler)

	//add CORS support
	if len(config.API.CORS.AllowedOrigins) > 0 {
		handler = cors.New(cors.Options{
			AllowedOrigins: config.API.CORS.AllowedOrigins,
			AllowedMethods: []string{"HEAD", "GET", "POST", "PUT"},
			AllowedHeaders: []string{"Content-Type", "User-Agent", "X-Auth-Token", "X-Limes-Cluster-Id"},
		}).Handler(handler)
	}

	//start HTTP server
	http.Handle("/", handler)
	logg.Info("listening on " + config.API.ListenAddress)
	return http.ListenAndServe(config.API.ListenAddress, nil)
}

////////////////////////////////////////////////////////////////////////////////
// task: test-scrape

func taskTestScrape(config core.Configuration, cluster *core.Cluster, args []string) error {
	if len(args) != 1 {
		printUsageAndExit()
	}

	var (
		domainUUID  string
		projectUUID string
	)
	err := db.DB.QueryRow(`
		SELECT d.uuid, p.uuid
		  FROM domains d JOIN projects p ON p.domain_id = d.id
		 WHERE p.uuid = $1 AND d.cluster_id = $2
	`, args[0], cluster.ID).Scan(&domainUUID, &projectUUID)
	if err == sql.ErrNoRows {
		return errors.New("no such project in this cluster")
	}

	result := make(map[string]map[string]core.ResourceData)

	for serviceType, plugin := range cluster.QuotaPlugins {
		provider, eo := cluster.ProviderClientForService(serviceType)
		data, err := plugin.Scrape(provider, eo, cluster.ID, domainUUID, projectUUID)
		if err != nil {
			logg.Error("scrape failed for %s: %s", serviceType, util.ErrorToString(err))
		}
		if data != nil {
			result[serviceType] = data
		}
	}

	dumpGeneratedPrometheusMetrics()

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

func dumpGeneratedPrometheusMetrics() {
	metricFamilies, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		if merr, ok := err.(prometheus.MultiError); ok {
			for _, err := range merr {
				logg.Error("error while gathering Prometheus metrics: " + err.Error())
			}
		} else {
			logg.Error("error while gathering Prometheus metrics: " + err.Error())
		}
	}

	for _, metricFamily := range metricFamilies {
		//skip metrics generated by prometheus/client-golang
		if strings.HasPrefix(*metricFamily.Name, "go_") || strings.HasPrefix(*metricFamily.Name, "process_") {
			continue
		}

		for _, metric := range metricFamily.Metric {
			labels := make(map[string]string)
			for _, label := range metric.Label {
				labels[*label.Name] = *label.Value
			}
			switch {
			case metric.Gauge != nil:
				logg.Info("generated gauge   %s %v %g", *metricFamily.Name, labels, *metric.Gauge.Value)
			case metric.Counter != nil:
				logg.Info("generated counter %s %v %g", *metricFamily.Name, labels, *metric.Counter.Value)
			default:
				logg.Info("generated metric  %s (do not know how to print type %d)", *metricFamily.Name, *metricFamily.Type)
			}
		}
	}
}

////////////////////////////////////////////////////////////////////////////////
// task: test-scan-capacity

func taskTestScanCapacity(config core.Configuration, cluster *core.Cluster, args []string) error {
	if len(args) != 0 {
		printUsageAndExit()
	}

	result := make(map[string]map[string]core.CapacityData)
	for capacitorID, plugin := range cluster.CapacityPlugins {
		provider, eo := cluster.ProviderClientForCapacitor(capacitorID)
		capacities, err := plugin.Scrape(provider, eo, cluster.ID)
		if err != nil {
			logg.Error("scan capacity with capacitor %s failed: %s", capacitorID, util.ErrorToString(err))
		}
		//merge capacities from this plugin into the overall capacity values map
		for serviceType, resources := range capacities {
			if _, ok := result[serviceType]; !ok {
				result[serviceType] = make(map[string]core.CapacityData)
			}
			for resourceName, value := range resources {
				result[serviceType][resourceName] = value
			}
		}
	}

	dumpGeneratedPrometheusMetrics()

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}
