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
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/mattes/migrate/migrate"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sapcc/limes/pkg/api"
	"github.com/sapcc/limes/pkg/collector"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/util"

	_ "github.com/mattes/migrate/driver/postgres"
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
	driver, err := limes.NewDriver(cluster)
	if err != nil {
		util.LogFatal(err.Error())
	}

	//select task
	var task func(limes.Configuration, limes.Driver, []string) error
	switch taskName {
	case "collect":
		task = taskCollect
	case "serve":
		task = taskServe
	case "sync-with-elektra":
		task = taskSyncWithElektra
	default:
		printUsageAndExit()
	}

	//run task
	err = task(config, driver, remainingArgs)
	if err != nil {
		util.LogFatal(err.Error())
	}
}

var usageMessage = strings.Replace(strings.TrimSpace(`
Usage:
\t%s migrate <config-file>
\t%s (collect|serve) <config-file> <cluster-id>
\t%s sync-with-elektra <config-file> <cluster-id> <elektra-dump-url>
`), `\t`, "\t", -1) + "\n"

func printUsageAndExit() {
	fmt.Fprintf(os.Stderr, usageMessage, os.Args[0], os.Args[0], os.Args[0])
	os.Exit(1)
}

////////////////////////////////////////////////////////////////////////////////
// task: migrate

func taskMigrate(config limes.Configuration) {
	errs, ok := migrate.UpSync(config.Database.Location, config.Database.MigrationsPath)
	if !ok {
		util.LogError("migration failed, see errors on stderr")
		for _, err := range errs {
			fmt.Fprintln(os.Stderr, err.Error())
		}
		os.Exit(1)
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

func taskCollect(config limes.Configuration, driver limes.Driver, args []string) error {
	if len(args) != 0 {
		printUsageAndExit()
	}
	cluster := driver.Cluster()

	//start scraping threads (NOTE: Many people use a pair of sync.WaitGroup and
	//stop channel to shutdown threads in a controlled manner. I decided against
	//that for now, and instead construct worker threads in such a way that they
	//can be terminated at any time without leaving the system in an inconsistent
	//state, mostly through usage of DB transactions.)
	for _, service := range cluster.Services {
		plugin := limes.GetQuotaPlugin(service.Type)
		if plugin == nil {
			util.LogError("skipping service %s: no suitable collector plugin found", service.Type)
			continue
		}
		c := collector.NewCollector(driver, plugin)
		go c.Scrape()
	}

	//complain about missing capacity plugins
	for _, capacitor := range cluster.Capacitors {
		plugin := limes.GetCapacityPlugin(capacitor.ID)
		if plugin == nil {
			util.LogError("skipping capacitor %s: no suitable collector plugin found", capacitor.ID)
			continue
		}
	}

	//start those collector threads which operate over all services simultaneously
	c := collector.NewCollector(driver, nil)
	go c.CheckConsistency()
	go c.ScanCapacity()
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
	return http.ListenAndServe(config.Collector.MetricsListenAddress, nil)
}

////////////////////////////////////////////////////////////////////////////////
// task: serve

func taskServe(config limes.Configuration, driver limes.Driver, args []string) error {
	if len(args) != 0 {
		printUsageAndExit()
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
	return http.ListenAndServe(config.API.ListenAddress, nil)
}

////////////////////////////////////////////////////////////////////////////////
// task: sync-with-elektra

func taskSyncWithElektra(config limes.Configuration, driver limes.Driver, args []string) error {
	if len(args) != 1 {
		printUsageAndExit()
	}

	http.DefaultClient.Transport = http.DefaultTransport
	for {
		util.LogInfo("Starting sync...")
		err := syncWithElektra(driver, args[0], false)
		if err == nil {
			util.LogInfo("Done.")
		} else {
			util.LogError(err.Error())
		}
		time.Sleep(15 * time.Minute)
	}
}

//Entry is an entry in the JSON returned by Elektra.
type Entry struct {
	DomainUUID   string `json:"domain_id"`
	ProjectUUID  string `json:"project_id"`
	ServiceType  string `json:"service"`
	ResourceName string `json:"resource"`
	Quota        uint64 `json:"approved_quota"`
}

func syncWithElektra(driver limes.Driver, elektraURL string, freshToken bool) error {
	//build request
	req, err := http.NewRequest("GET", elektraURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Auth-Token", driver.Client().TokenID)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	//check response
	switch {
	case resp.StatusCode == 200:
		//continue parsing the response below
	case resp.StatusCode == 401 && !freshToken:
		//our token might have expired - fetch a new one and retry
		err := driver.Client().ReauthFunc()
		if err != nil {
			return err
		}
		return syncWithElektra(driver, elektraURL, true)
	default:
		//unexpected response
		return fmt.Errorf("GET expected 200, got %s", resp.Status)
	}

	//parse JSON
	var entries []Entry
	err = json.NewDecoder(resp.Body).Decode(&entries)
	if err != nil {
		return err
	}

	//store data
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer db.RollbackUnlessCommitted(tx)

	stmtProject, err := tx.Prepare(`
		UPDATE project_resources SET quota = $1
		WHERE  name = $2 AND service_id IN (
			SELECT ps.id FROM project_services ps JOIN projects p ON ps.project_id = p.id
			WHERE  ps.type = $3 AND p.uuid = $4
		)
	`)
	if err != nil {
		return err
	}
	defer stmtProject.Close()

	stmtDomain, err := tx.Prepare(`
		SELECT ds.id FROM domain_services ds JOIN domains d ON ds.domain_id = d.id
		WHERE  ds.type = $1 AND d.uuid = $2
	`)
	if err != nil {
		return err
	}
	defer stmtDomain.Close()

	for _, entry := range entries {
		if entry.ProjectUUID != "" {
			_, err = stmtProject.Exec(entry.Quota, entry.ResourceName, entry.ServiceType, entry.ProjectUUID)
			if err != nil {
				return err
			}
			continue
		}

		var serviceID int64
		err = stmtDomain.QueryRow(entry.ServiceType, entry.DomainUUID).Scan(&serviceID)
		if err != nil {
			if err == sql.ErrNoRows {
				//skip services not yet supported by Limes
				continue
			}
			return err
		}

		record := &db.DomainResource{
			ServiceID: serviceID,
			Name:      entry.ResourceName,
			Quota:     entry.Quota,
		}

		rowsAffected, err := tx.Update(record)
		if err != nil {
			return err
		}
		if rowsAffected == 0 {
			err = tx.Insert(record)
			if err != nil {
				return err
			}
		}

	}
	return tx.Commit()
}
