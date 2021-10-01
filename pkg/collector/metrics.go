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

package collector

import (
	"database/sql"
	"encoding/json"
	"math/big"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/limes"
	"github.com/sapcc/limes/pkg/core"
	"github.com/sapcc/limes/pkg/db"
)

////////////////////////////////////////////////////////////////////////////////
// collector metrics

var scrapeSuccessCounter = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "limes_successful_scrapes",
		Help: "Counter for successful quota scrape operations per Keystone project.",
	},
	[]string{"os_cluster", "service", "service_name"},
)

var scrapeFailedCounter = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "limes_failed_scrapes",
		Help: "Counter for failed quota scrape operations per Keystone project.",
	},
	[]string{"os_cluster", "service", "service_name"},
)

var scrapeSuspendedCounter = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "limes_suspended_scrapes",
		Help: "Counter for suspended quota scrape operations per Keystone project.",
	},
	[]string{"os_cluster", "service", "service_name"},
)

var projectDiscoverySuccessCounter = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "limes_successful_project_discoveries",
		Help: "Counter for successful project discovery operations per Keystone domain.",
	},
	[]string{"os_cluster", "domain", "domain_id"},
)

var projectDiscoveryFailedCounter = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "limes_failed_project_discoveries",
		Help: "Counter for failed project discovery operations per Keystone domain.",
	},
	[]string{"os_cluster", "domain", "domain_id"},
)

var domainDiscoverySuccessCounter = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "limes_successful_domain_discoveries",
		Help: "Counter for successful domain discovery operations.",
	},
	[]string{"os_cluster"},
)

var domainDiscoveryFailedCounter = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "limes_failed_domain_discoveries",
		Help: "Counter for failed domain discovery operations.",
	},
	[]string{"os_cluster"},
)

var clusterCapacitorSuccessCounter = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "limes_successful_capacity_scrapes",
		Help: "Counter for successful cluster capacity scrapes.",
	},
	[]string{"os_cluster", "capacitor"},
)

var clusterCapacitorFailedCounter = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "limes_failed_capacity_scrapes",
		Help: "Counter for failed cluster capacity scrapes.",
	},
	[]string{"os_cluster", "capacitor"},
)

var ratesScrapeSuccessCounter = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "limes_successful_rate_scrapes",
		Help: "Counter for successful rate scrape operations per Keystone project.",
	},
	[]string{"os_cluster", "service", "service_name"},
)

var ratesScrapeFailedCounter = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "limes_failed_rate_scrapes",
		Help: "Counter for failed rate scrape operations per Keystone project.",
	},
	[]string{"os_cluster", "service", "service_name"},
)

var ratesScrapeSuspendedCounter = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "limes_suspended_rate_scrapes",
		Help: "Counter for suspended rate scrape operations per Keystone project.",
	},
	[]string{"os_cluster", "service", "service_name"},
)

func init() {
	prometheus.MustRegister(scrapeSuccessCounter)
	prometheus.MustRegister(scrapeFailedCounter)
	prometheus.MustRegister(scrapeSuspendedCounter)
	prometheus.MustRegister(projectDiscoverySuccessCounter)
	prometheus.MustRegister(projectDiscoveryFailedCounter)
	prometheus.MustRegister(domainDiscoverySuccessCounter)
	prometheus.MustRegister(domainDiscoveryFailedCounter)
	prometheus.MustRegister(clusterCapacitorSuccessCounter)
	prometheus.MustRegister(clusterCapacitorFailedCounter)
	prometheus.MustRegister(ratesScrapeSuccessCounter)
	prometheus.MustRegister(ratesScrapeFailedCounter)
	prometheus.MustRegister(ratesScrapeSuspendedCounter)
}

////////////////////////////////////////////////////////////////////////////////
// scraped_at aggregate metrics

var minScrapedAtGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_oldest_scraped_at",
		Help: "Oldest (i.e. smallest) scraped_at timestamp for any project given a certain service in a certain OpenStack cluster.",
	},
	[]string{"os_cluster", "service", "service_name"},
)

var maxScrapedAtGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_newest_scraped_at",
		Help: "Newest (i.e. largest) scraped_at timestamp for any project given a certain service in a certain OpenStack cluster.",
	},
	[]string{"os_cluster", "service", "service_name"},
)

var minRatesScrapedAtGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_oldest_rates_scraped_at",
		Help: "Oldest (i.e. smallest) rates_scraped_at timestamp for any project given a certain service in a certain OpenStack cluster.",
	},
	[]string{"os_cluster", "service", "service_name"},
)

var maxRatesScrapedAtGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_newest_rates_scraped_at",
		Help: "Newest (i.e. largest) rates_scraped_at timestamp for any project given a certain service in a certain OpenStack cluster.",
	},
	[]string{"os_cluster", "service", "service_name"},
)

//AggregateMetricsCollector is a prometheus.Collector that submits
//dynamically-calculated aggregate metrics about scraping progress.
type AggregateMetricsCollector struct {
	Cluster *core.Cluster
}

//Describe implements the prometheus.Collector interface.
func (c *AggregateMetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	minScrapedAtGauge.Describe(ch)
	maxScrapedAtGauge.Describe(ch)
	minRatesScrapedAtGauge.Describe(ch)
	maxRatesScrapedAtGauge.Describe(ch)
}

var scrapedAtAggregateQuery = db.SimplifyWhitespaceInSQL(`
	SELECT ps.type, MIN(ps.scraped_at), MAX(ps.scraped_at), MIN(ps.rates_scraped_at), MAX(ps.rates_scraped_at)
	  FROM domains d
	  JOIN projects p ON p.domain_id = d.id
	  JOIN project_services ps ON ps.project_id = p.id
	 WHERE d.cluster_id = $1 AND ps.scraped_at IS NOT NULL
	 GROUP BY ps.type
`)

//Collect implements the prometheus.Collector interface.
func (c *AggregateMetricsCollector) Collect(ch chan<- prometheus.Metric) {
	//NOTE: I use NewConstMetric() instead of storing the values in the GaugeVec
	//instances because it is faster.

	descCh := make(chan *prometheus.Desc, 1)
	minScrapedAtGauge.Describe(descCh)
	minScrapedAtDesc := <-descCh
	maxScrapedAtGauge.Describe(descCh)
	maxScrapedAtDesc := <-descCh
	minRatesScrapedAtGauge.Describe(descCh)
	minRatesScrapedAtDesc := <-descCh
	maxRatesScrapedAtGauge.Describe(descCh)
	maxRatesScrapedAtDesc := <-descCh

	queryArgs := []interface{}{c.Cluster.ID}
	err := db.ForeachRow(db.DB, scrapedAtAggregateQuery, queryArgs, func(rows *sql.Rows) error {
		var (
			serviceType       string
			minScrapedAt      *time.Time
			maxScrapedAt      *time.Time
			minRatesScrapedAt *time.Time
			maxRatesScrapedAt *time.Time
		)
		err := rows.Scan(&serviceType, &minScrapedAt, &maxScrapedAt, &minRatesScrapedAt, &maxRatesScrapedAt)
		if err != nil {
			return err
		}

		plugin := c.Cluster.QuotaPlugins[serviceType]
		if plugin == nil {
			return nil
		}
		serviceName := plugin.ServiceInfo().ProductName

		if len(plugin.Resources()) > 0 {
			ch <- prometheus.MustNewConstMetric(
				minScrapedAtDesc,
				prometheus.GaugeValue, timeAsUnixOrZero(minScrapedAt),
				c.Cluster.ID, serviceType, serviceName,
			)
			ch <- prometheus.MustNewConstMetric(
				maxScrapedAtDesc,
				prometheus.GaugeValue, timeAsUnixOrZero(maxScrapedAt),
				c.Cluster.ID, serviceType, serviceName,
			)
		}
		if len(plugin.Rates()) > 0 {
			ch <- prometheus.MustNewConstMetric(
				minRatesScrapedAtDesc,
				prometheus.GaugeValue, timeAsUnixOrZero(minRatesScrapedAt),
				c.Cluster.ID, serviceType, serviceName,
			)
			ch <- prometheus.MustNewConstMetric(
				maxRatesScrapedAtDesc,
				prometheus.GaugeValue, timeAsUnixOrZero(maxRatesScrapedAt),
				c.Cluster.ID, serviceType, serviceName,
			)
		}
		return nil
	})
	if err != nil {
		logg.Error("collect cluster aggregate metrics failed: " + err.Error())
	}
}

func timeAsUnixOrZero(t *time.Time) float64 {
	if t == nil {
		return 0
	}
	return float64(t.Unix())
}

////////////////////////////////////////////////////////////////////////////////
// capacity plugin metrics

var capacityPluginMetricsOkGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_capacity_plugin_metrics_ok",
		Help: "Whether capacity plugin metrics were rendered successfully for a particular capacitor. Only present when the capacitor emits metrics.",
	},
	[]string{"os_cluster", "capacitor"},
)

//CapacityPluginMetricsCollector is a prometheus.Collector that submits metrics
//which are specific to the selected capacity plugins.
type CapacityPluginMetricsCollector struct {
	Cluster *core.Cluster
	//When .Override is set, the DB is bypassed and only the given
	//CapacityPluginMetricsInstances are considered. This is used for testing only.
	Override []CapacityPluginMetricsInstance
}

//CapacityPluginMetricsInstance describes a single project service for which plugin
//metrics are submitted. It appears in type CapacityPluginMetricsCollector.
type CapacityPluginMetricsInstance struct {
	CapacitorID       string
	SerializedMetrics string
}

//Describe implements the prometheus.Collector interface.
func (c *CapacityPluginMetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	capacityPluginMetricsOkGauge.Describe(ch)
	for _, plugin := range c.Cluster.CapacityPlugins {
		plugin.DescribeMetrics(ch)
	}
}

var capacitySerializedMetricsGetQuery = db.SimplifyWhitespaceInSQL(`
	SELECT capacitor_id, serialized_metrics
	  FROM cluster_capacitors
	 WHERE cluster_id = $1 AND serialized_metrics != ''
`)

//Collect implements the prometheus.Collector interface.
func (c *CapacityPluginMetricsCollector) Collect(ch chan<- prometheus.Metric) {
	descCh := make(chan *prometheus.Desc, 1)
	capacityPluginMetricsOkGauge.Describe(descCh)
	pluginMetricsOkDesc := <-descCh

	if c.Override != nil {
		for _, instance := range c.Override {
			c.collectOneCapacitor(ch, pluginMetricsOkDesc, instance)
		}
		return
	}

	queryArgs := []interface{}{c.Cluster.ID}
	err := db.ForeachRow(db.DB, capacitySerializedMetricsGetQuery, queryArgs, func(rows *sql.Rows) error {
		var i CapacityPluginMetricsInstance
		err := rows.Scan(&i.CapacitorID, &i.SerializedMetrics)
		if err == nil {
			c.collectOneCapacitor(ch, pluginMetricsOkDesc, i)
		}
		return err
	})
	if err != nil {
		logg.Error("collect capacity plugin metrics failed: " + err.Error())
	}
}

func (c *CapacityPluginMetricsCollector) collectOneCapacitor(ch chan<- prometheus.Metric, pluginMetricsOkDesc *prometheus.Desc, instance CapacityPluginMetricsInstance) {
	plugin := c.Cluster.CapacityPlugins[instance.CapacitorID]
	if plugin == nil {
		return
	}
	err := plugin.CollectMetrics(ch, c.Cluster.ID, instance.SerializedMetrics)
	successAsFloat := 1.0
	if err != nil {
		successAsFloat = 0.0
		//errors in plugin.CollectMetrics() are not fatal: we record a failure in
		//the metrics and keep going with the other project services
		logg.Error("while collecting capacity metrics for capacitor %s: %s",
			instance.CapacitorID, err.Error())
	}
	ch <- prometheus.MustNewConstMetric(
		pluginMetricsOkDesc,
		prometheus.GaugeValue, successAsFloat,
		c.Cluster.ID, instance.CapacitorID,
	)
}

////////////////////////////////////////////////////////////////////////////////
// quota plugin metrics

var quotaPluginMetricsOkGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_plugin_metrics_ok",
		Help: "Whether quota plugin metrics were rendered successfully for a particular project service. Only present when the project service emits metrics.",
	},
	[]string{"os_cluster", "domain", "domain_id", "project", "project_id", "service"},
)

//QuotaPluginMetricsCollector is a prometheus.Collector that submits metrics
//which are specific to the selected quota plugins.
type QuotaPluginMetricsCollector struct {
	Cluster *core.Cluster
	//When .Override is set, the DB is bypassed and only the given
	//QuotaPluginMetricsInstances are considered. This is used for testing only.
	Override []QuotaPluginMetricsInstance
}

//QuotaPluginMetricsInstance describes a single project service for which plugin
//metrics are submitted. It appears in type QuotaPluginMetricsCollector.
type QuotaPluginMetricsInstance struct {
	Project           core.KeystoneProject
	ServiceType       string
	SerializedMetrics string
}

//Describe implements the prometheus.Collector interface.
func (c *QuotaPluginMetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	quotaPluginMetricsOkGauge.Describe(ch)
	for _, plugin := range c.Cluster.QuotaPlugins {
		plugin.DescribeMetrics(ch)
	}
}

var quotaSerializedMetricsGetQuery = db.SimplifyWhitespaceInSQL(`
	SELECT d.name, d.uuid, p.name, p.uuid, p.parent_uuid, ps.type, ps.serialized_metrics
	  FROM domains d
	  JOIN projects p ON p.domain_id = d.id
	  JOIN project_services ps ON ps.project_id = p.id
	 WHERE d.cluster_id = $1 AND ps.serialized_metrics != ''
`)

//Collect implements the prometheus.Collector interface.
func (c *QuotaPluginMetricsCollector) Collect(ch chan<- prometheus.Metric) {
	descCh := make(chan *prometheus.Desc, 1)
	quotaPluginMetricsOkGauge.Describe(descCh)
	pluginMetricsOkDesc := <-descCh

	if c.Override != nil {
		for _, instance := range c.Override {
			c.collectOneProjectService(ch, pluginMetricsOkDesc, instance)
		}
		return
	}

	queryArgs := []interface{}{c.Cluster.ID}
	err := db.ForeachRow(db.DB, quotaSerializedMetricsGetQuery, queryArgs, func(rows *sql.Rows) error {
		var i QuotaPluginMetricsInstance
		err := rows.Scan(
			&i.Project.Domain.Name, &i.Project.Domain.UUID,
			&i.Project.Name, &i.Project.UUID, &i.Project.ParentUUID,
			&i.ServiceType, &i.SerializedMetrics)
		if err == nil {
			c.collectOneProjectService(ch, pluginMetricsOkDesc, i)
		}
		return err
	})
	if err != nil {
		logg.Error("collect quota plugin metrics failed: " + err.Error())
	}
}

func (c *QuotaPluginMetricsCollector) collectOneProjectService(ch chan<- prometheus.Metric, pluginMetricsOkDesc *prometheus.Desc, instance QuotaPluginMetricsInstance) {
	plugin := c.Cluster.QuotaPlugins[instance.ServiceType]
	if plugin == nil {
		return
	}
	err := plugin.CollectMetrics(ch, c.Cluster.ID, instance.Project, instance.SerializedMetrics)
	successAsFloat := 1.0
	if err != nil {
		successAsFloat = 0.0
		//errors in plugin.CollectMetrics() are not fatal: we record a failure in
		//the metrics and keep going with the other project services
		logg.Error("while collecting plugin metrics for service %s in project %s: %s",
			instance.ServiceType, instance.Project.UUID, err.Error())
	}
	ch <- prometheus.MustNewConstMetric(
		pluginMetricsOkDesc,
		prometheus.GaugeValue, successAsFloat,
		c.Cluster.ID, instance.Project.Domain.Name, instance.Project.Domain.UUID, instance.Project.Name, instance.Project.UUID, instance.ServiceType,
	)
}

////////////////////////////////////////////////////////////////////////////////
// data metrics

var clusterCapacityGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_cluster_capacity",
		Help: "Reported capacity of a Limes resource for an OpenStack cluster.",
	},
	[]string{"os_cluster", "shared", "service", "resource"},
)

var clusterCapacityPerAZGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_cluster_capacity_per_az",
		Help: "Reported capacity of a Limes resource for an OpenStack cluster in a specific availability zone.",
	},
	[]string{"os_cluster", "availability_zone", "shared", "service", "resource"},
)

var clusterUsagePerAZGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_cluster_usage_per_az",
		Help: "Actual usage of a Limes resource for an OpenStack cluster in a specific availability zone.",
	},
	[]string{"os_cluster", "availability_zone", "shared", "service", "resource"},
)

var domainQuotaGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_domain_quota",
		Help: "Assigned quota of a Limes resource for an OpenStack domain.",
	},
	[]string{"os_cluster", "domain", "domain_id", "service", "resource"},
)

var projectQuotaGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_project_quota",
		Help: "Assigned quota of a Limes resource for an OpenStack project.",
	},
	[]string{"os_cluster", "domain", "domain_id", "project", "project_id", "service", "resource"},
)

var projectBackendQuotaGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_project_backendquota",
		Help: "Actual quota of a Limes resource for an OpenStack project.",
	},
	[]string{"os_cluster", "domain", "domain_id", "project", "project_id", "service", "resource"},
)

var projectUsageGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_project_usage",
		Help: "Actual (logical) usage of a Limes resource for an OpenStack project.",
	},
	[]string{"os_cluster", "domain", "domain_id", "project", "project_id", "service", "resource"},
)

var projectPhysicalUsageGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_project_physical_usage",
		Help: "Actual (physical) usage of a Limes resource for an OpenStack project.",
	},
	[]string{"os_cluster", "domain", "domain_id", "project", "project_id", "service", "resource"},
)

var projectRateUsageGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_project_rate_usage",
		Help: "Usage of a Limes rate for an OpenStack project. These are counters that never reset.",
	},
	[]string{"os_cluster", "domain", "domain_id", "project", "project_id", "service", "rate"},
)

var unitConversionGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_unit_multiplier",
		Help: "Conversion factor that a value of this resource must be multiplied" +
			" with to obtain the base unit (e.g. bytes). For use with Grafana when" +
			" only the base unit can be configured because of templating.",
	},
	[]string{"service", "resource"},
)

//DataMetricsCollector is a prometheus.Collector that submits
//quota/usage/backend quota from an OpenStack cluster as Prometheus metrics.
type DataMetricsCollector struct {
	Cluster      *core.Cluster
	ReportZeroes bool
}

//Describe implements the prometheus.Collector interface.
func (c *DataMetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	clusterCapacityGauge.Describe(ch)
	clusterCapacityPerAZGauge.Describe(ch)
	clusterUsagePerAZGauge.Describe(ch)
	domainQuotaGauge.Describe(ch)
	projectQuotaGauge.Describe(ch)
	projectBackendQuotaGauge.Describe(ch)
	projectUsageGauge.Describe(ch)
	projectPhysicalUsageGauge.Describe(ch)
	projectRateUsageGauge.Describe(ch)
	unitConversionGauge.Describe(ch)
}

var clusterMetricsQuery = db.SimplifyWhitespaceInSQL(`
	SELECT cs.cluster_id, cs.type, cr.name, cr.capacity, cr.capacity_per_az
	  FROM cluster_services cs
	  JOIN cluster_resources cr ON cr.service_id = cs.id
	 WHERE cs.cluster_id = $1 OR cs.cluster_id = 'shared'
`)

var domainMetricsQuery = db.SimplifyWhitespaceInSQL(`
	SELECT d.name, d.uuid, ds.type, dr.name, dr.quota
	  FROM domains d
	  JOIN domain_services ds ON ds.domain_id = d.id
	  JOIN domain_resources dr ON dr.service_id = ds.id
	 WHERE d.cluster_id = $1
`)

var projectMetricsQuery = db.SimplifyWhitespaceInSQL(`
	SELECT d.name, d.uuid, p.name, p.uuid, ps.type, pr.name, pr.quota, pr.backend_quota, pr.usage, pr.physical_usage
	  FROM domains d
	  JOIN projects p ON p.domain_id = d.id
	  JOIN project_services ps ON ps.project_id = p.id
	  JOIN project_resources pr ON pr.service_id = ps.id
	 WHERE d.cluster_id = $1
`)

var projectRateMetricsQuery = db.SimplifyWhitespaceInSQL(`
	SELECT d.name, d.uuid, p.name, p.uuid, ps.type, pra.name, pra.usage_as_bigint
	  FROM domains d
	  JOIN projects p ON p.domain_id = d.id
	  JOIN project_services ps ON ps.project_id = p.id
	  JOIN project_rates pra ON pra.service_id = ps.id
	 WHERE d.cluster_id = $1 AND pra.usage_as_bigint != ''
`)

//Collect implements the prometheus.Collector interface.
func (c *DataMetricsCollector) Collect(ch chan<- prometheus.Metric) {
	//NOTE: I use NewConstMetric() instead of storing the values in the GaugeVec
	//instances,
	//
	//1. because it is faster.
	//2. because this automatically handles deleted projects/domains correctly.
	//   (Their metrics just disappear when Prometheus scrapes next time.)

	//fetch Descs for all metrics
	descCh := make(chan *prometheus.Desc, 1)
	clusterCapacityGauge.Describe(descCh)
	clusterCapacityDesc := <-descCh
	clusterCapacityPerAZGauge.Describe(descCh)
	clusterCapacityPerAZDesc := <-descCh
	clusterUsagePerAZGauge.Describe(descCh)
	clusterUsagePerAZDesc := <-descCh
	domainQuotaGauge.Describe(descCh)
	domainQuotaDesc := <-descCh
	projectQuotaGauge.Describe(descCh)
	projectQuotaDesc := <-descCh
	projectBackendQuotaGauge.Describe(descCh)
	projectBackendQuotaDesc := <-descCh
	projectUsageGauge.Describe(descCh)
	projectUsageDesc := <-descCh
	projectPhysicalUsageGauge.Describe(descCh)
	projectPhysicalUsageDesc := <-descCh
	projectRateUsageGauge.Describe(descCh)
	projectRateUsageDesc := <-descCh
	unitConversionGauge.Describe(descCh)
	unitConversionDesc := <-descCh

	//fetch values for cluster level
	capacityReported := make(map[string]map[string]bool)
	queryArgs := []interface{}{c.Cluster.ID}
	err := db.ForeachRow(db.DB, clusterMetricsQuery, queryArgs, func(rows *sql.Rows) error {
		var (
			clusterID     string
			serviceType   string
			resourceName  string
			capacity      uint64
			capacityPerAZ string
		)
		err := rows.Scan(&clusterID, &serviceType, &resourceName, &capacity, &capacityPerAZ)
		if err != nil {
			return err
		}
		var sharedString string
		if clusterID == "shared" {
			sharedString = "true"
			if !c.Cluster.IsServiceShared[serviceType] {
				return nil //continue with next row
			}
		} else {
			sharedString = "false"
		}

		behavior := c.Cluster.BehaviorForResource(serviceType, resourceName, "")
		overcommitFactor := float64(behavior.OvercommitFactor)
		if overcommitFactor == 0 {
			overcommitFactor = 1
		}

		if capacityPerAZ != "" {
			azReports := make(limes.ClusterAvailabilityZoneReports)
			err := json.Unmarshal([]byte(capacityPerAZ), &azReports)
			if err != nil {
				return err
			}
			for _, report := range azReports {
				ch <- prometheus.MustNewConstMetric(
					clusterCapacityPerAZDesc,
					prometheus.GaugeValue, float64(report.Capacity)*overcommitFactor,
					c.Cluster.ID, report.Name, sharedString, serviceType, resourceName,
				)
				if report.Usage != 0 {
					ch <- prometheus.MustNewConstMetric(
						clusterUsagePerAZDesc,
						prometheus.GaugeValue, float64(report.Usage),
						c.Cluster.ID, report.Name, sharedString, serviceType, resourceName,
					)
				}
			}

		}

		ch <- prometheus.MustNewConstMetric(
			clusterCapacityDesc,
			prometheus.GaugeValue, float64(capacity)*overcommitFactor,
			c.Cluster.ID, sharedString, serviceType, resourceName,
		)

		_, exists := capacityReported[serviceType]
		if !exists {
			capacityReported[serviceType] = make(map[string]bool)
		}
		capacityReported[serviceType][resourceName] = true

		return nil
	})
	if err != nil {
		logg.Error("collect cluster data metrics failed: " + err.Error())
	}

	//make sure that a cluster capacity value is reported for each resource (the
	//corresponding time series might otherwise be missing if capacity scraping
	//fails)
	for serviceType, quotaPlugin := range c.Cluster.QuotaPlugins {
		for _, res := range quotaPlugin.Resources() {
			if capacityReported[serviceType][res.Name] {
				continue
			}

			sharedString := "false"
			if c.Cluster.IsServiceShared[serviceType] {
				sharedString = "true"
			}
			ch <- prometheus.MustNewConstMetric(
				clusterCapacityDesc,
				prometheus.GaugeValue, 0,
				c.Cluster.ID, sharedString, serviceType, res.Name,
			)
		}
	}

	//fetch values for domain level
	err = db.ForeachRow(db.DB, domainMetricsQuery, queryArgs, func(rows *sql.Rows) error {
		var (
			domainName   string
			domainUUID   string
			serviceType  string
			resourceName string
			quota        uint64
		)
		err := rows.Scan(&domainName, &domainUUID, &serviceType, &resourceName, &quota)
		if err != nil {
			return err
		}
		ch <- prometheus.MustNewConstMetric(
			domainQuotaDesc,
			prometheus.GaugeValue, float64(quota),
			c.Cluster.ID, domainName, domainUUID, serviceType, resourceName,
		)
		return nil
	})
	if err != nil {
		logg.Error("collect domain metrics failed: " + err.Error())
	}

	//fetch values for project level (quota/usage)
	err = db.ForeachRow(db.DB, projectMetricsQuery, queryArgs, func(rows *sql.Rows) error {
		var (
			domainName    string
			domainUUID    string
			projectName   string
			projectUUID   string
			serviceType   string
			resourceName  string
			quota         *uint64
			backendQuota  *int64
			usage         uint64
			physicalUsage *uint64
		)
		err := rows.Scan(&domainName, &domainUUID, &projectName, &projectUUID, &serviceType, &resourceName, &quota, &backendQuota, &usage, &physicalUsage)
		if err != nil {
			return err
		}

		if quota != nil {
			if c.ReportZeroes || *quota != 0 {
				ch <- prometheus.MustNewConstMetric(
					projectQuotaDesc,
					prometheus.GaugeValue, float64(*quota),
					c.Cluster.ID, domainName, domainUUID, projectName, projectUUID, serviceType, resourceName,
				)
			}
		}
		if backendQuota != nil {
			if c.ReportZeroes || *backendQuota != 0 {
				ch <- prometheus.MustNewConstMetric(
					projectBackendQuotaDesc,
					prometheus.GaugeValue, float64(*backendQuota),
					c.Cluster.ID, domainName, domainUUID, projectName, projectUUID, serviceType, resourceName,
				)
			}
		}
		if c.ReportZeroes || usage != 0 {
			ch <- prometheus.MustNewConstMetric(
				projectUsageDesc,
				prometheus.GaugeValue, float64(usage),
				c.Cluster.ID, domainName, domainUUID, projectName, projectUUID, serviceType, resourceName,
			)
		}
		if physicalUsage != nil {
			if c.ReportZeroes || *physicalUsage != 0 {
				ch <- prometheus.MustNewConstMetric(
					projectPhysicalUsageDesc,
					prometheus.GaugeValue, float64(*physicalUsage),
					c.Cluster.ID, domainName, domainUUID, projectName, projectUUID, serviceType, resourceName,
				)
			}
		}
		return nil
	})
	if err != nil {
		logg.Error("collect project metrics failed: " + err.Error())
	}

	//fetch metadata for services/resources
	for serviceType, quotaPlugin := range c.Cluster.QuotaPlugins {
		for _, resource := range quotaPlugin.Resources() {
			_, multiplier := resource.Unit.Base()
			ch <- prometheus.MustNewConstMetric(
				unitConversionDesc,
				prometheus.GaugeValue, float64(multiplier),
				serviceType, resource.Name,
			)
		}
	}

	//fetch values for project level (rate usage)
	_ = db.ForeachRow(db.DB, projectRateMetricsQuery, queryArgs, func(rows *sql.Rows) error {
		var (
			domainName    string
			domainUUID    string
			projectName   string
			projectUUID   string
			serviceType   string
			rateName      string
			usageAsBigint string
		)
		err := rows.Scan(&domainName, &domainUUID, &projectName, &projectUUID, &serviceType, &rateName, &usageAsBigint)
		if err != nil {
			return err
		}
		usageAsBigFloat, _, err := big.NewFloat(0).Parse(usageAsBigint, 10)
		if err != nil {
			return err
		}
		usageAsFloat, _ := usageAsBigFloat.Float64()

		if c.ReportZeroes || usageAsFloat != 0 {
			ch <- prometheus.MustNewConstMetric(
				projectRateUsageDesc,
				prometheus.GaugeValue, usageAsFloat,
				c.Cluster.ID, domainName, domainUUID, projectName, projectUUID, serviceType, rateName,
			)
		}
		return nil
	})
}
