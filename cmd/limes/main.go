/*******************************************************************************
*
* Copyright 2017-2020 SAP SE
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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	policy "github.com/databus23/goslo.policy"
	"github.com/dlmiddlecote/sqlstats"
	"github.com/gophercloud/gophercloud"
	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/cors"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpee"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/respondwith"
	"github.com/sapcc/limes/pkg/api"
	"github.com/sapcc/limes/pkg/collector"
	"github.com/sapcc/limes/pkg/core"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/util"
	"gopkg.in/yaml.v2"

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

	//all tasks have the <cluster-id> as os.Args[3]
	if len(os.Args) < 4 {
		printUsageAndExit()
	}
	clusterID, remainingArgs := os.Args[3], os.Args[4:]

	//connect to cluster
	cluster, exists := config.Clusters[clusterID]
	if !exists {
		logg.Fatal("no such cluster configured: " + clusterID)
	}
	err := cluster.Connect()
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
	case "test-get-quota":
		task = taskTestGetQuota
	case "test-get-rates":
		task = taskTestGetRates
	case "test-set-quota":
		task = taskTestSetQuota
	case "test-scan-capacity":
		task = taskTestScanCapacity
	default:
		printUsageAndExit()
	}

	//run task
	err = task(config, cluster, remainingArgs)
	if err != nil {
		logg.Fatal(util.ErrorToString(err))
	}
}

var usageMessage = strings.Replace(strings.TrimSpace(`
Usage:
\t%s (collect|serve) <config-file> <cluster-id>
\t%s test-get-quota <config-file> <cluster-id> <project-id> <service-type>
\t%s test-get-rates <config-file> <cluster-id> <project-id> <service-type> [<prev-serialized-state>]
\t%s test-set-quota <config-file> <cluster-id> <project-id> <service-type> <resource-name>=<integer-value>...
\t%s test-scan-capacity <config-file> <cluster-id> <capacitor>
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

	//connect to database
	err := db.Init()
	if err != nil {
		logg.Fatal(err.Error())
	}
	prometheus.MustRegister(sqlstats.NewStatsCollector("limes", db.DB.Db))

	//start scraping threads (NOTE: Many people use a pair of sync.WaitGroup and
	//stop channel to shutdown threads in a controlled manner. I decided against
	//that for now, and instead construct worker threads in such a way that they
	//can be terminated at any time without leaving the system in an inconsistent
	//state, mostly through usage of DB transactions.)
	for _, plugin := range cluster.QuotaPlugins {
		c := collector.NewCollector(cluster, plugin)
		go c.Scrape()
		go c.ScrapeRates()
	}

	//start those collector threads which operate over all services simultaneously
	c := collector.NewCollector(cluster, nil)
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
	prometheus.MustRegister(&collector.AggregateMetricsCollector{Cluster: cluster})
	prometheus.MustRegister(&collector.CapacityPluginMetricsCollector{Cluster: cluster})
	prometheus.MustRegister(&collector.QuotaPluginMetricsCollector{Cluster: cluster})
	if exposeMetrics, _ := strconv.ParseBool("LIMES_COLLECTOR_DATA_METRICS_EXPOSE"); exposeMetrics {
		skipZero, _ := strconv.ParseBool("LIMES_COLLECTOR_DATA_METRICS_SKIP_ZERO")
		prometheus.MustRegister(&collector.DataMetricsCollector{
			Cluster:      cluster,
			ReportZeroes: !skipZero,
		})
	}
	http.Handle("/metrics", promhttp.Handler())
	metricsListenAddr := util.EnvOrDefault("LIMES_COLLECTOR_METRICS_LISTEN_ADDRESS", ":8080")
	logg.Info("listening on " + metricsListenAddr)
	return httpee.ListenAndServeContext(httpee.ContextWithSIGINT(context.Background(), 10*time.Second), metricsListenAddr, nil)
}

////////////////////////////////////////////////////////////////////////////////
// task: serve

func taskServe(config core.Configuration, cluster *core.Cluster, args []string) error {
	if len(args) != 0 {
		printUsageAndExit()
	}

	//connect to database
	err := db.Init()
	if err != nil {
		logg.Fatal(err.Error())
	}
	prometheus.MustRegister(sqlstats.NewStatsCollector("limes", db.DB.Db))

	//load oslo.policy file
	policyEnforcer, err := loadPolicyFile(util.EnvOrDefault("LIMES_API_POLICY_PATH", "/etc/limes/policy.yaml"))
	if err != nil {
		logg.Fatal("could not load policy file: %s", err.Error())
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
	v1Router, v1VersionData := api.NewV1Router(cluster, config, policyEnforcer)
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
	exceptCodeStrings := strings.Split(os.Getenv("LIMES_API_REQUEST_LOG_EXCEPT_STATUS_CODES"), ",")
	var exceptCodes []int
	for _, v := range exceptCodeStrings {
		v := strings.TrimSpace(v)
		code, err := strconv.Atoi(v)
		if err != nil {
			logg.Fatal("could not parse LIMES_API_REQUEST_LOG_EXCEPT_STATUS_CODES: %s", err.Error())
		}
		exceptCodes = append(exceptCodes, code)
	}
	handler = logg.Middleware{ExceptStatusCodes: exceptCodes}.Wrap(handler)

	//add CORS support
	allowedOriginStr := strings.ReplaceAll(os.Getenv("LIMES_API_CORS_ALLOWED_ORIGINS"), " ", "")
	allowedOrigins := strings.Split(allowedOriginStr, "||")
	if len(allowedOrigins) > 0 {
		handler = cors.New(cors.Options{
			AllowedOrigins: allowedOrigins,
			AllowedMethods: []string{"HEAD", "GET", "POST", "PUT"},
			AllowedHeaders: []string{"Content-Type", "User-Agent", "X-Auth-Token", "X-Limes-Cluster-Id"},
		}).Handler(handler)
	}

	//start HTTP server
	http.Handle("/", handler)
	apiListenAddr := util.EnvOrDefault("LIMES_API_LISTEN_ADDRESS", ":80")
	logg.Info("listening on " + apiListenAddr)
	return httpee.ListenAndServeContext(httpee.ContextWithSIGINT(context.Background(), 10*time.Second), apiListenAddr, nil)
}

////////////////////////////////////////////////////////////////////////////////
// tasks: test quota plugin

func taskTestGetQuota(config core.Configuration, cluster *core.Cluster, args []string) error {
	if len(args) != 2 {
		printUsageAndExit()
	}

	serviceType := args[1]
	provider, eo := getServiceProviderClient(cluster, serviceType)
	project, err := findProjectForTesting(cluster, provider, eo, args[0])
	if err != nil {
		return err
	}

	result, serializedMetrics, err := cluster.QuotaPlugins[serviceType].Scrape(provider, eo, project)
	if err != nil {
		return err
	}

	for resourceName := range result {
		if !cluster.HasResource(serviceType, resourceName) {
			return fmt.Errorf("scrape returned data for unknown resource: %s/%s", serviceType, resourceName)
		}
	}

	prometheus.MustRegister(&collector.QuotaPluginMetricsCollector{
		Cluster: cluster,
		Override: []collector.QuotaPluginMetricsInstance{{
			Project:           project,
			ServiceType:       serviceType,
			SerializedMetrics: serializedMetrics,
		}},
	})
	dumpGeneratedPrometheusMetrics()

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

func taskTestGetRates(config core.Configuration, cluster *core.Cluster, args []string) error {
	var prevSerializedState string
	switch len(args) {
	case 2:
		prevSerializedState = ""
	case 3:
		prevSerializedState = args[2]
	default:
		printUsageAndExit()
	}

	serviceType := args[1]
	provider, eo := getServiceProviderClient(cluster, serviceType)
	project, err := findProjectForTesting(cluster, provider, eo, args[0])
	if err != nil {
		return err
	}

	result, serializedState, err := cluster.QuotaPlugins[serviceType].ScrapeRates(provider, eo, project, prevSerializedState)
	if err != nil {
		return err
	}
	if serializedState != "" {
		logg.Info("scrape returned new serialized state: %s", serializedState)
	}

	for rateName := range result {
		if !cluster.HasUsageForRate(serviceType, rateName) {
			return fmt.Errorf("scrape returned data for unknown rate: %s/%s", serviceType, rateName)
		}
	}

	dumpGeneratedPrometheusMetrics()

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

func getServiceProviderClient(cluster *core.Cluster, serviceType string) (*gophercloud.ProviderClient, gophercloud.EndpointOpts) {
	if !cluster.HasService(serviceType) {
		logg.Fatal("unknown service type: %s", serviceType)
	}
	return cluster.ProviderClientForService(serviceType)
}

func findProjectForTesting(cluster *core.Cluster, client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, projectUUID string) (core.KeystoneProject, error) {
	domains, err := cluster.DiscoveryPlugin.ListDomains(client, eo)
	if err != nil {
		return core.KeystoneProject{}, err
	}
	for _, d := range domains {
		projects, err := cluster.DiscoveryPlugin.ListProjects(client, eo, d)
		if err != nil {
			return core.KeystoneProject{}, err
		}
		for _, p := range projects {
			if projectUUID == p.UUID {
				return p, nil
			}
		}
	}
	return core.KeystoneProject{}, errors.New("no such project in this cluster")
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

func taskTestSetQuota(config core.Configuration, cluster *core.Cluster, args []string) error {
	if len(args) < 3 {
		printUsageAndExit()
	}

	serviceType := args[1]
	provider, eo := getServiceProviderClient(cluster, serviceType)
	project, err := findProjectForTesting(cluster, provider, eo, args[0])
	if err != nil {
		return err
	}

	quotaValueRx := regexp.MustCompile(`^([^=]+)=(\d+)$`)
	quotaValues := make(map[string]uint64)
	for _, arg := range args[2:] {
		match := quotaValueRx.FindStringSubmatch(arg)
		if match == nil {
			printUsageAndExit()
		}
		val, err := strconv.ParseUint(match[2], 10, 64)
		if err != nil {
			logg.Fatal(err.Error())
		}
		quotaValues[match[1]] = val
	}

	return cluster.QuotaPlugins[serviceType].SetQuota(provider, eo, project, quotaValues)
}

////////////////////////////////////////////////////////////////////////////////
// task: test-scan-capacity

func taskTestScanCapacity(config core.Configuration, cluster *core.Cluster, args []string) error {
	if len(args) != 1 {
		printUsageAndExit()
	}

	capacitorID := args[0]
	plugin := cluster.CapacityPlugins[capacitorID]
	if plugin == nil {
		logg.Fatal("unknown capacitor: %s", capacitorID)
	}

	provider, eo := cluster.ProviderClientForCapacitor(capacitorID)
	capacities, serializedMetrics, err := plugin.Scrape(provider, eo)
	if err != nil {
		logg.Error("Scrape failed: %s", util.ErrorToString(err))
		capacities = nil
	}

	prometheus.MustRegister(&collector.CapacityPluginMetricsCollector{
		Cluster: cluster,
		Override: []collector.CapacityPluginMetricsInstance{{
			CapacitorID:       capacitorID,
			SerializedMetrics: serializedMetrics,
		}},
	})
	dumpGeneratedPrometheusMetrics()

	for srvType, srvCapacities := range capacities {
		for resName := range srvCapacities {
			if !cluster.HasResource(srvType, resName) {
				logg.Error("Scrape reported capacity for unknown resource: %s/%s", srvType, resName)
			}
		}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(capacities)
}

////////////////////////////////////////////////////////////////////////////////
// Helper functions
func loadPolicyFile(path string) (gopherpolicy.Enforcer, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rules map[string]string
	err = yaml.Unmarshal(bytes, &rules)
	if err != nil {
		return nil, err
	}
	return policy.NewEnforcer(rules)
}
