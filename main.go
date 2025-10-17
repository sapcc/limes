// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/cors"
	"github.com/sapcc/go-api-declarations/bininfo"
	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/gophercloudext"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/httpapi/pprofapi"
	"github.com/sapcc/go-bits/httpext"
	"github.com/sapcc/go-bits/jobloop"
	"github.com/sapcc/go-bits/liquidapi"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/must"
	"github.com/sapcc/go-bits/osext"
	"go.uber.org/automaxprocs/maxprocs"

	"github.com/sapcc/limes/internal/api"
	"github.com/sapcc/limes/internal/collector"
	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/datamodel"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/liquids/archer"
	"github.com/sapcc/limes/internal/liquids/cinder"
	"github.com/sapcc/limes/internal/liquids/cronus"
	"github.com/sapcc/limes/internal/liquids/designate"
	"github.com/sapcc/limes/internal/liquids/ironic"
	"github.com/sapcc/limes/internal/liquids/manila"
	"github.com/sapcc/limes/internal/liquids/neutron"
	"github.com/sapcc/limes/internal/liquids/nova"
	"github.com/sapcc/limes/internal/liquids/octavia"
	"github.com/sapcc/limes/internal/liquids/swift"
	"github.com/sapcc/limes/internal/util"

	. "github.com/majewsky/gg/option"
)

func main() {
	bininfo.HandleVersionArgument()
	logg.ShowDebug = osext.GetenvBool("LIMES_DEBUG")
	undoMaxprocs := must.Return(maxprocs.Set(maxprocs.Logger(logg.Debug)))
	defer undoMaxprocs()

	// setup http.DefaultTransport overrides
	wrap := httpext.WrapTransport(&http.DefaultTransport)
	wrap.SetInsecureSkipVerify(osext.GetenvBool("LIMES_INSECURE")) // for debugging with mitmproxy etc. (DO NOT SET IN PRODUCTION)
	wrap.Attach(util.AddLoggingRoundTripper)
	// NOTE: wrap.SetOverrideUserAgent() needs to be delayed until further down when we have figured out the task name.

	ctx := httpext.ContextWithSIGINT(context.Background(), 100*time.Millisecond)

	// when running as a liquid, branch off early; liquids do not share most of
	// the initialization steps that the core components need
	if len(os.Args) > 2 && os.Args[1] == "liquid" {
		if len(os.Args) != 3 {
			printUsageAndExit(1)
		}

		liquidName := os.Args[2]
		bininfo.SetTaskName("liquid-" + liquidName)
		wrap.SetOverrideUserAgent(bininfo.Component(), bininfo.VersionOr("rolling"))

		opts := liquidapi.RunOpts{
			TakesConfiguration:         false,
			ServiceInfoRefreshInterval: 5 * time.Minute,
			MaxConcurrentRequests:      5,
			DefaultListenAddress:       ":80",
		}
		switch liquidName {
		case "archer":
			opts.ServiceInfoRefreshInterval = 0
			must.Succeed(liquidapi.Run(ctx, &archer.Logic{}, opts))
		case "cinder":
			opts.TakesConfiguration = true
			must.Succeed(liquidapi.Run(ctx, &cinder.Logic{}, opts))
		case "cronus":
			opts.ServiceInfoRefreshInterval = 0
			must.Succeed(liquidapi.Run(ctx, &cronus.Logic{}, opts))
		case "designate":
			opts.ServiceInfoRefreshInterval = 0
			must.Succeed(liquidapi.Run(ctx, &designate.Logic{}, opts))
		case "ironic":
			opts.TakesConfiguration = true
			must.Succeed(liquidapi.Run(ctx, &ironic.Logic{}, opts))
		case "manila":
			opts.TakesConfiguration = true
			must.Succeed(liquidapi.Run(ctx, &manila.Logic{}, opts))
		case "neutron":
			must.Succeed(liquidapi.Run(ctx, &neutron.Logic{}, opts))
		case "nova":
			opts.TakesConfiguration = true
			must.Succeed(liquidapi.Run(ctx, &nova.Logic{}, opts))
		case "octavia":
			must.Succeed(liquidapi.Run(ctx, &octavia.Logic{}, opts))
		case "swift":
			opts.ServiceInfoRefreshInterval = 0
			must.Succeed(liquidapi.Run(ctx, &swift.Logic{}, opts))
		default:
			logg.Fatal("no liquid implementation available for %q", liquidName)
		}
		return
	}

	// first two arguments must be task name and configuration file
	if slices.Contains(os.Args, "--help") {
		printUsageAndExit(0)
	}
	if len(os.Args) < 3 {
		printUsageAndExit(1)
	}
	taskName, configPath, remainingArgs := os.Args[1], os.Args[2], os.Args[3:]
	bininfo.SetTaskName(taskName)
	wrap.SetOverrideUserAgent(bininfo.Component(), bininfo.VersionOr("rolling"))

	// connect to OpenStack
	provider, eo, err := gophercloudext.NewProviderClient(ctx, nil)
	must.Succeed(err)

	// load configuration and connect to cluster
	dbm := db.InitORM(must.Return(db.Init()))
	cluster, errs := core.NewClusterFromJSON(must.Return(os.ReadFile(configPath)), time.Now, dbm, taskName == "collect")
	errs.LogFatalIfError()
	errs = cluster.Connect(ctx, provider, eo, core.LiquidClientFactory(provider, eo))
	errs.LogFatalIfError()

	// select task
	switch taskName {
	case "collect":
		taskCollect(ctx, cluster, remainingArgs, provider, eo)
	case "serve":
		taskServe(ctx, cluster, remainingArgs, provider, eo)
	case "serve-data-metrics":
		taskServeDataMetrics(ctx, cluster, remainingArgs)
	default:
		printUsageAndExit(1)
	}
}

var usageMessage = strings.ReplaceAll(strings.TrimSpace(`
Usage:
\t%s (collect|serve|serve-data-metrics) <config-file>
\t%s liquid <service-type>
`), `\t`, "\t") + "\n"

func printUsageAndExit(exitCode int) {
	fmt.Fprintln(os.Stderr, strings.ReplaceAll(usageMessage, "%s", os.Args[0]))
	os.Exit(exitCode)
}

////////////////////////////////////////////////////////////////////////////////
// task: collect

func taskCollect(ctx context.Context, cluster *core.Cluster, args []string, provider *gophercloud.ProviderClient, _ gophercloud.EndpointOpts) {
	if len(args) != 0 {
		printUsageAndExit(1)
	}
	isAuthoritative := osext.GetenvBool("LIMES_AUTHORITATIVE")

	// setup mail client if requested
	mailClient := None[collector.MailClient]()
	if mailConfig, ok := cluster.Config.MailNotifications.Unpack(); ok {
		mailClient = Some(must.Return(collector.NewMailClient(provider, mailConfig.Endpoint)))
	}

	// start scraping threads (NOTE: Many people use a pair of sync.WaitGroup and
	// stop channel to shutdown threads in a controlled manner. I decided against
	// that for now, and instead construct worker threads in such a way that they
	// can be terminated at any time without leaving the system in an inconsistent
	// state, mostly through usage of DB transactions.)
	c := collector.NewCollector(cluster, generateAuditor(ctx))
	scrapeJob := c.ScrapeJob(nil)
	syncQuotaToBackendJob := c.SyncQuotaToBackendJob(nil)
	for serviceType := range cluster.LiquidConnections {
		opt := jobloop.WithLabel("service_type", string(serviceType))
		go scrapeJob.Run(ctx, opt)
		if isAuthoritative {
			go syncQuotaToBackendJob.Run(ctx, opt)
		}
	}

	// start those collector threads which operate over all services simultaneously
	go c.ApplyQuotaOverridesJob(nil).Run(ctx)
	go c.CapacityScrapeJob(nil).Run(ctx)
	go c.CheckConsistencyJob(nil).Run(ctx)
	go c.CleanupOldCommitmentsJob(nil).Run(ctx)
	go c.ScanDomainsAndProjectsJob(nil).Run(ctx)

	// start mail processing if requested
	if mc, ok := mailClient.Unpack(); ok {
		if !osext.GetenvBool("LIMES_BLOCK_EXPIRY_NOTIFICATIONS") {
			// ^ This is a hidden flag to block expiry notifications from being sent if necessary.
			go c.ExpiringCommitmentNotificationJob(nil).Run(ctx)
		}
		go c.MailDeliveryJob(nil, mc).Run(ctx)
	}

	// use main thread to emit Prometheus metrics
	prometheus.MustRegister(&collector.AggregateMetricsCollector{Cluster: cluster, DB: cluster.DB})
	prometheus.MustRegister(&collector.CapacityCollectionMetricsCollector{Cluster: cluster, DB: cluster.DB})
	prometheus.MustRegister(&collector.UsageCollectionMetricsCollector{Cluster: cluster, DB: cluster.DB})
	mux := http.NewServeMux()
	mux.Handle("/", httpapi.Compose(
		pprofapi.API{IsAuthorized: pprofapi.IsRequestFromLocalhost},
		httpapi.HealthCheckAPI{
			SkipRequestLog: true,
			Check: func() error {
				return cluster.DB.Db.PingContext(ctx)
			},
		},
	))
	mux.Handle("/metrics", promhttp.Handler())

	metricsListenAddr := osext.GetenvOrDefault("LIMES_COLLECTOR_METRICS_LISTEN_ADDRESS", ":8080")
	must.Succeed(httpext.ListenAndServeContext(ctx, metricsListenAddr, mux))
}

////////////////////////////////////////////////////////////////////////////////
// task: serve

func taskServe(ctx context.Context, cluster *core.Cluster, args []string, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) {
	if len(args) != 0 {
		printUsageAndExit(1)
	}

	// collect all API endpoints and middlewares
	tokenValidator := must.Return(api.NewTokenValidator(provider, eo))
	corsMiddleware := cors.New(cors.Options{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"HEAD", "GET", "POST", "PUT", "DELETE"},
		AllowedHeaders: []string{"Content-Type", "User-Agent", "X-Auth-Token", "X-Limes-Cluster-Id", "X-Limes-V2-Api-Preview", "Transfer-Token"},
	})
	mux := http.NewServeMux()
	mux.Handle("/", httpapi.Compose(
		api.NewV1API(cluster, tokenValidator, generateAuditor(ctx), time.Now, datamodel.GenerateTransferToken, datamodel.GenerateProjectCommitmentUUID),
		pprofapi.API{IsAuthorized: pprofapi.IsRequestFromLocalhost},
		httpapi.WithGlobalMiddleware(api.ForbidClusterIDHeader),
		httpapi.WithGlobalMiddleware(corsMiddleware.Handler),
	))
	mux.Handle("/metrics", promhttp.Handler())

	// start HTTP server
	apiListenAddr := osext.GetenvOrDefault("LIMES_API_LISTEN_ADDRESS", ":80")
	must.Succeed(httpext.ListenAndServeContext(ctx, apiListenAddr, mux))
}

////////////////////////////////////////////////////////////////////////////////
// task: serve data metrics

func taskServeDataMetrics(ctx context.Context, cluster *core.Cluster, args []string) {
	if len(args) != 0 {
		printUsageAndExit(1)
	}

	// serve data metrics
	skipZero := osext.GetenvBool("LIMES_DATA_METRICS_SKIP_ZERO")
	dmr := collector.DataMetricsReporter{
		Cluster:      cluster,
		DB:           cluster.DB,
		ReportZeroes: !skipZero,
	}

	mux := http.NewServeMux()
	mux.Handle("/", httpapi.Compose(
		pprofapi.API{IsAuthorized: pprofapi.IsRequestFromLocalhost},
		httpapi.HealthCheckAPI{
			SkipRequestLog: true,
			Check: func() error {
				return cluster.DB.Db.PingContext(ctx)
			},
		},
	))
	mux.Handle("/metrics", &dmr)

	metricsListenAddr := osext.GetenvOrDefault("LIMES_DATA_METRICS_LISTEN_ADDRESS", ":8080")
	must.Succeed(httpext.ListenAndServeContext(ctx, metricsListenAddr, mux))
}

func generateAuditor(ctx context.Context) audittools.Auditor {
	// connect to Hermes RabbitMQ if requested
	auditor := audittools.NewNullAuditor()
	if os.Getenv("LIMES_AUDIT_RABBITMQ_QUEUE_NAME") != "" {
		auditor = must.Return(audittools.NewAuditor(ctx, audittools.AuditorOpts{
			EnvPrefix: "LIMES_AUDIT_RABBITMQ",
			Observer: audittools.Observer{
				TypeURI: "service/resources",
				Name:    bininfo.Component(),
				ID:      audittools.GenerateUUID(),
			},
		}))
	}
	return auditor
}
