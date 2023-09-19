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

	"github.com/go-gorp/gorp/v3"
	"github.com/prometheus/client_golang/prometheus"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
)

////////////////////////////////////////////////////////////////////////////////
// scraped_at aggregate metrics

var minScrapedAtGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_oldest_scraped_at",
		Help: "Oldest (i.e. smallest) scraped_at timestamp for any project given a certain service in a certain OpenStack cluster.",
	},
	[]string{"service", "service_name"},
)

var maxScrapedAtGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_newest_scraped_at",
		Help: "Newest (i.e. largest) scraped_at timestamp for any project given a certain service in a certain OpenStack cluster.",
	},
	[]string{"service", "service_name"},
)

var minRatesScrapedAtGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_oldest_rates_scraped_at",
		Help: "Oldest (i.e. smallest) rates_scraped_at timestamp for any project given a certain service in a certain OpenStack cluster.",
	},
	[]string{"service", "service_name"},
)

var maxRatesScrapedAtGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_newest_rates_scraped_at",
		Help: "Newest (i.e. largest) rates_scraped_at timestamp for any project given a certain service in a certain OpenStack cluster.",
	},
	[]string{"service", "service_name"},
)

// AggregateMetricsCollector is a prometheus.Collector that submits
// dynamically-calculated aggregate metrics about scraping progress.
type AggregateMetricsCollector struct {
	Cluster *core.Cluster
	DB      *gorp.DbMap
}

// Describe implements the prometheus.Collector interface.
func (c *AggregateMetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	minScrapedAtGauge.Describe(ch)
	maxScrapedAtGauge.Describe(ch)
	minRatesScrapedAtGauge.Describe(ch)
	maxRatesScrapedAtGauge.Describe(ch)
}

var scrapedAtAggregateQuery = sqlext.SimplifyWhitespace(`
	SELECT type, MIN(scraped_at), MAX(scraped_at), MIN(rates_scraped_at), MAX(rates_scraped_at)
	  FROM project_services
	 WHERE scraped_at IS NOT NULL
	 GROUP BY type
`)

// Collect implements the prometheus.Collector interface.
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

	err := sqlext.ForeachRow(c.DB, scrapedAtAggregateQuery, nil, func(rows *sql.Rows) error {
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
		serviceName := plugin.ServiceInfo(serviceType).ProductName

		if len(plugin.Resources()) > 0 {
			ch <- prometheus.MustNewConstMetric(
				minScrapedAtDesc,
				prometheus.GaugeValue, timeAsUnixOrZero(minScrapedAt),
				serviceType, serviceName,
			)
			ch <- prometheus.MustNewConstMetric(
				maxScrapedAtDesc,
				prometheus.GaugeValue, timeAsUnixOrZero(maxScrapedAt),
				serviceType, serviceName,
			)
		}
		if len(plugin.Rates()) > 0 {
			ch <- prometheus.MustNewConstMetric(
				minRatesScrapedAtDesc,
				prometheus.GaugeValue, timeAsUnixOrZero(minRatesScrapedAt),
				serviceType, serviceName,
			)
			ch <- prometheus.MustNewConstMetric(
				maxRatesScrapedAtDesc,
				prometheus.GaugeValue, timeAsUnixOrZero(maxRatesScrapedAt),
				serviceType, serviceName,
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
	[]string{"capacitor"},
)

// CapacityPluginMetricsCollector is a prometheus.Collector that submits metrics
// which are specific to the selected capacity plugins.
type CapacityPluginMetricsCollector struct {
	Cluster *core.Cluster
	DB      *gorp.DbMap
	//When .Override is set, the DB is bypassed and only the given
	//CapacityPluginMetricsInstances are considered. This is used for testing only.
	Override []CapacityPluginMetricsInstance
}

// CapacityPluginMetricsInstance describes a single project service for which plugin
// metrics are submitted. It appears in type CapacityPluginMetricsCollector.
type CapacityPluginMetricsInstance struct {
	CapacitorID       string
	SerializedMetrics string
}

// Describe implements the prometheus.Collector interface.
func (c *CapacityPluginMetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	capacityPluginMetricsOkGauge.Describe(ch)
	for _, plugin := range c.Cluster.CapacityPlugins {
		plugin.DescribeMetrics(ch)
	}
}

var capacitySerializedMetricsGetQuery = sqlext.SimplifyWhitespace(`
	SELECT capacitor_id, serialized_metrics
	  FROM cluster_capacitors
	 WHERE serialized_metrics != ''
`)

// Collect implements the prometheus.Collector interface.
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

	err := sqlext.ForeachRow(c.DB, capacitySerializedMetricsGetQuery, nil, func(rows *sql.Rows) error {
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
	err := plugin.CollectMetrics(ch, instance.SerializedMetrics)
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
		instance.CapacitorID,
	)
}

////////////////////////////////////////////////////////////////////////////////
// quota plugin metrics

var quotaPluginMetricsOkGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_plugin_metrics_ok",
		Help: "Whether quota plugin metrics were rendered successfully for a particular project service. Only present when the project service emits metrics.",
	},
	[]string{"domain", "domain_id", "project", "project_id", "service"},
)

// QuotaPluginMetricsCollector is a prometheus.Collector that submits metrics
// which are specific to the selected quota plugins.
type QuotaPluginMetricsCollector struct {
	Cluster *core.Cluster
	DB      *gorp.DbMap
	//When .Override is set, the DB is bypassed and only the given
	//QuotaPluginMetricsInstances are considered. This is used for testing only.
	Override []QuotaPluginMetricsInstance
}

// QuotaPluginMetricsInstance describes a single project service for which plugin
// metrics are submitted. It appears in type QuotaPluginMetricsCollector.
type QuotaPluginMetricsInstance struct {
	Project           core.KeystoneProject
	ServiceType       string
	SerializedMetrics string
}

// Describe implements the prometheus.Collector interface.
func (c *QuotaPluginMetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	quotaPluginMetricsOkGauge.Describe(ch)
	for _, plugin := range c.Cluster.QuotaPlugins {
		plugin.DescribeMetrics(ch)
	}
}

var quotaSerializedMetricsGetQuery = sqlext.SimplifyWhitespace(`
	SELECT d.name, d.uuid, p.name, p.uuid, p.parent_uuid, ps.type, ps.serialized_metrics
	  FROM domains d
	  JOIN projects p ON p.domain_id = d.id
	  JOIN project_services ps ON ps.project_id = p.id
	 WHERE ps.serialized_metrics != ''
`)

// Collect implements the prometheus.Collector interface.
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

	err := sqlext.ForeachRow(c.DB, quotaSerializedMetricsGetQuery, nil, func(rows *sql.Rows) error {
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
	err := plugin.CollectMetrics(ch, instance.Project, instance.SerializedMetrics)
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
		instance.Project.Domain.Name, instance.Project.Domain.UUID, instance.Project.Name, instance.Project.UUID, instance.ServiceType,
	)
}

////////////////////////////////////////////////////////////////////////////////
// data metrics

var clusterCapacityGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_cluster_capacity",
		Help: "Reported capacity of a Limes resource for an OpenStack cluster.",
	},
	[]string{"service", "resource"},
)

var clusterCapacityPerAZGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_cluster_capacity_per_az",
		Help: "Reported capacity of a Limes resource for an OpenStack cluster in a specific availability zone.",
	},
	[]string{"availability_zone", "service", "resource"},
)

var clusterUsagePerAZGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_cluster_usage_per_az",
		Help: "Actual usage of a Limes resource for an OpenStack cluster in a specific availability zone.",
	},
	[]string{"availability_zone", "service", "resource"},
)

var domainQuotaGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_domain_quota",
		Help: "Assigned quota of a Limes resource for an OpenStack domain.",
	},
	[]string{"domain", "domain_id", "service", "resource"},
)

var projectQuotaGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_project_quota",
		Help: "Assigned quota of a Limes resource for an OpenStack project.",
	},
	[]string{"domain", "domain_id", "project", "project_id", "service", "resource"},
)

var projectBackendQuotaGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_project_backendquota",
		Help: "Actual quota of a Limes resource for an OpenStack project.",
	},
	[]string{"domain", "domain_id", "project", "project_id", "service", "resource"},
)

var projectUsageGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_project_usage",
		Help: "Actual (logical) usage of a Limes resource for an OpenStack project.",
	},
	[]string{"domain", "domain_id", "project", "project_id", "service", "resource"},
)

var projectPhysicalUsageGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_project_physical_usage",
		Help: "Actual (physical) usage of a Limes resource for an OpenStack project.",
	},
	[]string{"domain", "domain_id", "project", "project_id", "service", "resource"},
)

var projectRateUsageGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_project_rate_usage",
		Help: "Usage of a Limes rate for an OpenStack project. These are counters that never reset.",
	},
	[]string{"domain", "domain_id", "project", "project_id", "service", "rate"},
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

// DataMetricsCollector is a prometheus.Collector that submits
// quota/usage/backend quota from an OpenStack cluster as Prometheus metrics.
type DataMetricsCollector struct {
	Cluster      *core.Cluster
	DB           *gorp.DbMap
	ReportZeroes bool
}

// Describe implements the prometheus.Collector interface.
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

var clusterMetricsQuery = sqlext.SimplifyWhitespace(`
	SELECT cs.type, cr.name, cr.capacity, cr.capacity_per_az
	  FROM cluster_services cs
	  JOIN cluster_resources cr ON cr.service_id = cs.id
`)

var domainMetricsQuery = sqlext.SimplifyWhitespace(`
	SELECT d.name, d.uuid, ds.type, dr.name, dr.quota
	  FROM domains d
	  JOIN domain_services ds ON ds.domain_id = d.id
	  JOIN domain_resources dr ON dr.service_id = ds.id
`)

var projectMetricsQuery = sqlext.SimplifyWhitespace(`
	SELECT d.name, d.uuid, p.name, p.uuid, ps.type, pr.name, pr.quota, pr.backend_quota, pr.usage, pr.physical_usage
	  FROM domains d
	  JOIN projects p ON p.domain_id = d.id
	  JOIN project_services ps ON ps.project_id = p.id
	  JOIN project_resources pr ON pr.service_id = ps.id
`)

var projectRateMetricsQuery = sqlext.SimplifyWhitespace(`
	SELECT d.name, d.uuid, p.name, p.uuid, ps.type, pra.name, pra.usage_as_bigint
	  FROM domains d
	  JOIN projects p ON p.domain_id = d.id
	  JOIN project_services ps ON ps.project_id = p.id
	  JOIN project_rates pra ON pra.service_id = ps.id
	 WHERE pra.usage_as_bigint != ''
`)

// Collect implements the prometheus.Collector interface.
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
	err := sqlext.ForeachRow(c.DB, clusterMetricsQuery, nil, func(rows *sql.Rows) error {
		var (
			serviceType   string
			resourceName  string
			capacity      uint64
			capacityPerAZ string
		)
		err := rows.Scan(&serviceType, &resourceName, &capacity, &capacityPerAZ)
		if err != nil {
			return err
		}

		behavior := c.Cluster.BehaviorForResource(serviceType, resourceName, "")
		overcommitFactor := behavior.OvercommitFactor
		if overcommitFactor == 0 {
			overcommitFactor = 1
		}

		if capacityPerAZ != "" {
			azReports := make(limesresources.ClusterAvailabilityZoneReports)
			err := json.Unmarshal([]byte(capacityPerAZ), &azReports)
			if err != nil {
				return err
			}
			for _, report := range azReports {
				ch <- prometheus.MustNewConstMetric(
					clusterCapacityPerAZDesc,
					prometheus.GaugeValue, float64(report.Capacity)*overcommitFactor,
					report.Name, serviceType, resourceName,
				)
				if report.Usage != 0 {
					ch <- prometheus.MustNewConstMetric(
						clusterUsagePerAZDesc,
						prometheus.GaugeValue, float64(report.Usage),
						report.Name, serviceType, resourceName,
					)
				}
			}
		}

		ch <- prometheus.MustNewConstMetric(
			clusterCapacityDesc,
			prometheus.GaugeValue, float64(capacity)*overcommitFactor,
			serviceType, resourceName,
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

			ch <- prometheus.MustNewConstMetric(
				clusterCapacityDesc,
				prometheus.GaugeValue, 0,
				serviceType, res.Name,
			)
		}
	}

	//fetch values for domain level
	err = sqlext.ForeachRow(c.DB, domainMetricsQuery, nil, func(rows *sql.Rows) error {
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
			domainName, domainUUID, serviceType, resourceName,
		)
		return nil
	})
	if err != nil {
		logg.Error("collect domain metrics failed: " + err.Error())
	}

	//fetch values for project level (quota/usage)
	err = sqlext.ForeachRow(c.DB, projectMetricsQuery, nil, func(rows *sql.Rows) error {
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
					domainName, domainUUID, projectName, projectUUID, serviceType, resourceName,
				)
			}
		}
		if backendQuota != nil {
			if c.ReportZeroes || *backendQuota != 0 {
				ch <- prometheus.MustNewConstMetric(
					projectBackendQuotaDesc,
					prometheus.GaugeValue, float64(*backendQuota),
					domainName, domainUUID, projectName, projectUUID, serviceType, resourceName,
				)
			}
		}
		if c.ReportZeroes || usage != 0 {
			ch <- prometheus.MustNewConstMetric(
				projectUsageDesc,
				prometheus.GaugeValue, float64(usage),
				domainName, domainUUID, projectName, projectUUID, serviceType, resourceName,
			)
		}
		if physicalUsage != nil {
			if c.ReportZeroes || *physicalUsage != 0 {
				ch <- prometheus.MustNewConstMetric(
					projectPhysicalUsageDesc,
					prometheus.GaugeValue, float64(*physicalUsage),
					domainName, domainUUID, projectName, projectUUID, serviceType, resourceName,
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
	err = sqlext.ForeachRow(c.DB, projectRateMetricsQuery, nil, func(rows *sql.Rows) error {
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
				domainName, domainUUID, projectName, projectUUID, serviceType, rateName,
			)
		}
		return nil
	})
	if err != nil {
		logg.Error("collect project metrics failed: %s", err.Error())
	}
}
