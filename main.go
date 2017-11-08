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
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sapcc/limes/pkg/api"
	"github.com/sapcc/limes/pkg/collector"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/util"

	"github.com/mattes/migrate"
	_ "github.com/mattes/migrate/database/postgres"
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
	config := limes.NewConfiguration(configPath)

	//handle migrate task specially; it's the only one that does not need the
	//standard database and Keystone connections
	if taskName == "migrate" {
		taskMigrate(config)
		return
	}

	//all other tasks have the <cluster-id> as os.Args[3]
	if len(os.Args) < 4 {
		printUsageAndExit()
	}
	clusterID, remainingArgs := os.Args[3], os.Args[4:]

	//connect to database
	err := db.Init(config.Database)
	if err != nil {
		util.LogFatal(err.Error())
	}

	//connect to cluster
	cluster, exists := config.Clusters[clusterID]
	if !exists {
		util.LogFatal("no such cluster configured: " + clusterID)
	}
	err = cluster.Connect()
	if err != nil {
		util.LogFatal(err.Error())
	}

	//select task
	var task func(limes.Configuration, *limes.Cluster, []string) error
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
		util.LogFatal(err.Error())
	}
}

var usageMessage = strings.Replace(strings.TrimSpace(`
Usage:
\t%s migrate <config-file>
\t%s (collect|serve) <config-file> <cluster-id>
\t%s test-scrape <config-file> <cluster-id> <project-id>
\t%s test-scan-capacity <config-file> <cluster-id>
`), `\t`, "\t", -1) + "\n"

func printUsageAndExit() {
	fmt.Fprintln(os.Stderr, strings.Replace(usageMessage, "%s", os.Args[0], -1))
	os.Exit(1)
}

////////////////////////////////////////////////////////////////////////////////
// task: migrate

func taskMigrate(config limes.Configuration) {
	err := createDatabaseIfNotExist(config)
	if err != nil {
		util.LogError(err.Error())
		os.Exit(1)
	}

	m, err := config.Database.GetMigrate("go-bindata")
	if err == nil {
		err = m.Up()
	}
	if err != nil && err != migrate.ErrNoChange {
		util.LogFatal("migration failed: " + err.Error())
	}
}

var dbNotExistErrRx = regexp.MustCompile(`^pq: database "([^"]+)" does not exist$`)

func createDatabaseIfNotExist(config limes.Configuration) error {
	//check if the database exists
	db, err := sql.Open("postgres", config.Database.Location)
	if err == nil {
		//apparently the "database does not exist" error only occurs when trying to issue the first statement
		_, err = db.Exec("SELECT 1")
	}
	if err == nil {
		//nothing to do
		return db.Close()
	}
	match := dbNotExistErrRx.FindStringSubmatch(err.Error())
	if match == nil {
		//unexpected error
		return err
	}
	dbName := match[1]

	//remove the database name from the connection URL
	dbURL, err := url.Parse(config.Database.Location)
	if err != nil {
		return err
	}

	dbURL.Path = "/"
	db, err = sql.Open("postgres", dbURL.String())
	if err != nil {
		return err
	}
	defer db.Close()

	_, err = db.Exec("CREATE DATABASE " + dbName)
	return err
}

////////////////////////////////////////////////////////////////////////////////
// task: collect

func taskCollect(config limes.Configuration, cluster *limes.Cluster, args []string) error {
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
				util.LogError(err.Error())
			}
			time.Sleep(discoverInterval)
		}
	}()

	//use main thread to emit Prometheus metrics
	if config.Collector.ExposeDataMetrics {
		prometheus.MustRegister(&collector.DataMetricsCollector{Cluster: cluster})
	}
	http.Handle("/metrics", promhttp.Handler())
	util.LogInfo("listening on " + config.Collector.MetricsListenAddress)
	return http.ListenAndServe(config.Collector.MetricsListenAddress, nil)
}

////////////////////////////////////////////////////////////////////////////////
// task: serve

func taskServe(config limes.Configuration, cluster *limes.Cluster, args []string) error {
	if len(args) != 0 {
		printUsageAndExit()
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
		api.ReturnJSON(w, 300, allVersions)
	})

	//add Prometheus instrumentation
	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/", prometheus.InstrumentHandler("limes-serve", mainRouter))

	//start HTTP server
	util.LogInfo("listening on " + config.API.ListenAddress)
	return http.ListenAndServe(config.API.ListenAddress, nil)
}

////////////////////////////////////////////////////////////////////////////////
// task: test-scrape

func taskTestScrape(config limes.Configuration, cluster *limes.Cluster, args []string) error {
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

	result := make(map[string]map[string]limes.ResourceData)

	for serviceType, plugin := range cluster.QuotaPlugins {
		data, err := plugin.Scrape(cluster.ProviderClientForService(serviceType), domainUUID, projectUUID)
		if err != nil {
			util.LogError("scrape failed for %s: %s", serviceType, err.Error())
		}
		if data != nil {
			result[serviceType] = data
		}
	}

	return json.NewEncoder(os.Stdout).Encode(result)
}

////////////////////////////////////////////////////////////////////////////////
// task: test-scan-capacity

func taskTestScanCapacity(config limes.Configuration, cluster *limes.Cluster, args []string) error {
	if len(args) != 0 {
		printUsageAndExit()
	}

	result := make(map[string]map[string]uint64)
	for capacitorID, plugin := range cluster.CapacityPlugins {
		capacities, err := plugin.Scrape(cluster.ProviderClient())
		if err != nil {
			util.LogError("scan capacity with capacitor %s failed: %s", capacitorID, err.Error())
		}
		//merge capacities from this plugin into the overall capacity values map
		for serviceType, resources := range capacities {
			if _, ok := result[serviceType]; !ok {
				result[serviceType] = make(map[string]uint64)
			}
			for resourceName, value := range resources {
				result[serviceType][resourceName] = value
			}
		}
	}

	return json.NewEncoder(os.Stdout).Encode(result)
}
