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

	"github.com/dlmiddlecote/sqlstats"
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/utils/openstack/clientconfig"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/cors"
	"github.com/sapcc/go-api-declarations/bininfo"
	"github.com/sapcc/go-bits/errext"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/httpext"
	"github.com/sapcc/go-bits/jobloop"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/must"
	"github.com/sapcc/go-bits/osext"
	"go.uber.org/automaxprocs/maxprocs"

	"github.com/sapcc/limes/internal/api"
	"github.com/sapcc/limes/internal/collector"
	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"

	_ "github.com/sapcc/limes/internal/plugins"
)

var discoverInterval = 3 * time.Minute

func main() {
	logg.ShowDebug = osext.GetenvBool("LIMES_DEBUG")
	undoMaxprocs := must.Return(maxprocs.Set(maxprocs.Logger(logg.Debug)))
	defer undoMaxprocs()

	//first two arguments must be task name and configuration file
	if len(os.Args) < 3 {
		printUsageAndExit()
	}
	taskName, configPath, remainingArgs := os.Args[1], os.Args[2], os.Args[3:]
	bininfo.SetTaskName(taskName)

	//setup http.DefaultTransport overrides
	wrap := httpext.WrapTransport(&http.DefaultTransport)
	wrap.SetInsecureSkipVerify(osext.GetenvBool("LIMES_INSECURE")) //for debugging with mitmproxy etc. (DO NOT SET IN PRODUCTION)
	wrap.SetOverrideUserAgent(bininfo.Component(), bininfo.VersionOr("rolling"))
	wrap.Attach(util.AddLoggingRoundTripper)

	//connect to OpenStack
	ao, err := clientconfig.AuthOptions(nil)
	if err != nil {
		logg.Fatal("cannot find OpenStack credentials: " + err.Error())
	}
	ao.AllowReauth = true
	provider, err := openstack.AuthenticatedClient(*ao)
	if err != nil {
		logg.Fatal("cannot initialize OpenStack client: " + err.Error())
	}
	eo := gophercloud.EndpointOpts{
		Availability: gophercloud.Availability(os.Getenv("OS_INTERFACE")),
		Region:       os.Getenv("OS_REGION_NAME"),
	}

	//load configuration and connect to cluster
	cluster, errs := core.NewConfiguration(configPath)
	errs.LogFatalIfError()
	errs = cluster.Connect(provider, eo)
	errs.LogFatalIfError()
	api.StartAuditTrail()

	//select task
	switch taskName {
	case "collect":
		taskCollect(cluster, remainingArgs)
	case "serve":
		taskServe(cluster, remainingArgs, provider, eo)
	case "test-get-quota":
		taskTestGetQuota(cluster, remainingArgs)
	case "test-get-rates":
		taskTestGetRates(cluster, remainingArgs)
	case "test-set-quota":
		taskTestSetQuota(cluster, remainingArgs)
	case "test-scan-capacity":
		taskTestScanCapacity(cluster, remainingArgs)
	default:
		printUsageAndExit()
	}
}

var usageMessage = strings.Replace(strings.TrimSpace(`
Usage:
\t%s (collect|serve) <config-file>
\t%s test-get-quota <config-file> <project-id> <service-type>
\t%s test-get-rates <config-file> <project-id> <service-type> [<prev-serialized-state>]
\t%s test-set-quota <config-file> <project-id> <service-type> <resource-name>=<integer-value>...
\t%s test-scan-capacity <config-file> <capacitor>
`), `\t`, "\t", -1) + "\n"

func printUsageAndExit() {
	fmt.Fprintln(os.Stderr, strings.Replace(usageMessage, "%s", os.Args[0], -1))
	os.Exit(1)
}

////////////////////////////////////////////////////////////////////////////////
// task: collect

func taskCollect(cluster *core.Cluster, args []string) {
	if len(args) != 0 {
		printUsageAndExit()
	}

	ctx := httpext.ContextWithSIGINT(context.Background(), 10*time.Second)

	//connect to database
	dbm := must.Return(db.Init())
	prometheus.MustRegister(sqlstats.NewStatsCollector("limes", dbm.Db))

	//start scraping threads (NOTE: Many people use a pair of sync.WaitGroup and
	//stop channel to shutdown threads in a controlled manner. I decided against
	//that for now, and instead construct worker threads in such a way that they
	//can be terminated at any time without leaving the system in an inconsistent
	//state, mostly through usage of DB transactions.)
	c := collector.NewCollector(cluster, dbm)
	resourceScrapeJob := c.ResourceScrapeJob(nil)
	rateScrapeJob := c.RateScrapeJob(nil)
	for serviceType := range cluster.QuotaPlugins {
		opt := jobloop.WithLabel("service_type", serviceType)
		go resourceScrapeJob.Run(ctx, opt)
		go rateScrapeJob.Run(ctx, opt)
	}

	//start those collector threads which operate over all services simultaneously
	go c.CheckConsistencyJob(nil).Run(ctx)
	go c.ScanCapacity()
	go func() {
		for {
			_, err := c.ScanDomains(collector.ScanDomainsOpts{ScanAllProjects: true})
			if err != nil {
				logg.Error(util.UnpackError(err).Error())
			}
			time.Sleep(discoverInterval)
		}
	}()

	//use main thread to emit Prometheus metrics
	prometheus.MustRegister(&collector.AggregateMetricsCollector{Cluster: cluster, DB: dbm})
	prometheus.MustRegister(&collector.CapacityPluginMetricsCollector{Cluster: cluster, DB: dbm})
	prometheus.MustRegister(&collector.QuotaPluginMetricsCollector{Cluster: cluster, DB: dbm})
	if osext.GetenvBool("LIMES_COLLECTOR_DATA_METRICS_EXPOSE") {
		skipZero := osext.GetenvBool("LIMES_COLLECTOR_DATA_METRICS_SKIP_ZERO")
		prometheus.MustRegister(&collector.DataMetricsCollector{
			Cluster:      cluster,
			DB:           dbm,
			ReportZeroes: !skipZero,
		})
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	metricsListenAddr := osext.GetenvOrDefault("LIMES_COLLECTOR_METRICS_LISTEN_ADDRESS", ":8080")
	must.Succeed(httpext.ListenAndServeContext(ctx, metricsListenAddr, mux))
}

////////////////////////////////////////////////////////////////////////////////
// task: serve

func taskServe(cluster *core.Cluster, args []string, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) {
	if len(args) != 0 {
		printUsageAndExit()
	}

	//connect to database
	dbm := must.Return(db.Init())
	prometheus.MustRegister(sqlstats.NewStatsCollector("limes", dbm.Db))

	//collect all API endpoints and middlewares
	tokenValidator := must.Return(api.NewTokenValidator(provider, eo))
	corsMiddleware := cors.New(cors.Options{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"HEAD", "GET", "POST", "PUT"},
		AllowedHeaders: []string{"Content-Type", "User-Agent", "X-Auth-Token", "X-Limes-Cluster-Id"},
	})
	mux := http.NewServeMux()
	mux.Handle("/", httpapi.Compose(
		api.NewV1API(cluster, dbm, tokenValidator),
		httpapi.WithGlobalMiddleware(api.ForbidClusterIDHeader),
		httpapi.WithGlobalMiddleware(corsMiddleware.Handler),
	))
	mux.Handle("/metrics", promhttp.Handler())

	//start HTTP server
	apiListenAddr := osext.GetenvOrDefault("LIMES_API_LISTEN_ADDRESS", ":80")
	ctx := httpext.ContextWithSIGINT(context.Background(), 10*time.Second)
	must.Succeed(httpext.ListenAndServeContext(ctx, apiListenAddr, mux))
}

////////////////////////////////////////////////////////////////////////////////
// tasks: test quota plugin

func taskTestGetQuota(cluster *core.Cluster, args []string) {
	if len(args) != 2 {
		printUsageAndExit()
	}

	serviceType := args[1]
	project := must.Return(findProjectForTesting(cluster, args[0]))

	if _, ok := cluster.QuotaPlugins[serviceType]; !ok {
		logg.Fatal("unknown service type: %s", serviceType)
	}

	result, serializedMetrics, err := cluster.QuotaPlugins[serviceType].Scrape(project)
	must.Succeed(err)

	for resourceName := range result {
		if !cluster.HasResource(serviceType, resourceName) {
			logg.Fatal("scrape returned data for unknown resource: %s/%s", serviceType, resourceName)
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
	must.Succeed(enc.Encode(result))
}

func taskTestGetRates(cluster *core.Cluster, args []string) {
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
	project := must.Return(findProjectForTesting(cluster, args[0]))

	result, serializedState, err := cluster.QuotaPlugins[serviceType].ScrapeRates(project, prevSerializedState)
	must.Succeed(err)
	if serializedState != "" {
		logg.Info("scrape returned new serialized state: %s", serializedState)
	}

	for rateName := range result {
		if !cluster.HasUsageForRate(serviceType, rateName) {
			logg.Fatal("scrape returned data for unknown rate: %s/%s", serviceType, rateName)
		}
	}

	dumpGeneratedPrometheusMetrics()

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	must.Succeed(enc.Encode(result))
}

func findProjectForTesting(cluster *core.Cluster, projectUUID string) (core.KeystoneProject, error) {
	domains, err := cluster.DiscoveryPlugin.ListDomains()
	if err != nil {
		return core.KeystoneProject{}, util.UnpackError(err)
	}
	for _, d := range domains {
		projects, err := cluster.DiscoveryPlugin.ListProjects(d)
		if err != nil {
			return core.KeystoneProject{}, util.UnpackError(err)
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
		if merr, ok := errext.As[prometheus.MultiError](err); ok {
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

func taskTestSetQuota(cluster *core.Cluster, args []string) {
	if len(args) < 3 {
		printUsageAndExit()
	}

	serviceType := args[1]
	project := must.Return(findProjectForTesting(cluster, args[0]))

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

	must.Succeed(cluster.QuotaPlugins[serviceType].SetQuota(project, quotaValues))
}

////////////////////////////////////////////////////////////////////////////////
// task: test-scan-capacity

func taskTestScanCapacity(cluster *core.Cluster, args []string) {
	if len(args) != 1 {
		printUsageAndExit()
	}

	capacitorID := args[0]
	plugin := cluster.CapacityPlugins[capacitorID]
	if plugin == nil {
		logg.Fatal("unknown capacitor: %s", capacitorID)
	}

	capacities, serializedMetrics, err := plugin.Scrape()
	if err != nil {
		logg.Error("Scrape failed: %s", util.UnpackError(err).Error())
		capacities = nil
	}

	if serializedMetrics != "" {
		logg.Info("serializedMetrics: %s", serializedMetrics)
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
				logg.Fatal("Scrape reported capacity for unknown resource: %s/%s", srvType, resName)
			}
		}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	must.Succeed(enc.Encode(capacities))
}
