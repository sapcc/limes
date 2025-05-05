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
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-gorp/gorp/v3"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/respondwith"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

////////////////////////////////////////////////////////////////////////////////
// scraped_at aggregate metrics

// TODO: When merging these metrics together while merging the resource
// scraping and rate scraping loops, please also get rid of the duplicate label
// `service_name` that is currently retained for backwards compatibility (which
// is to say, I didn't care to assess the impact of removing it yet).
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
	// instances because it is faster.

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
			serviceType       db.ServiceType
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

		if len(plugin.ServiceInfo().Resources) > 0 {
			ch <- prometheus.MustNewConstMetric(
				minScrapedAtDesc,
				prometheus.GaugeValue, timeAsUnixOrZero(minScrapedAt),
				string(serviceType), string(serviceType),
			)
			ch <- prometheus.MustNewConstMetric(
				maxScrapedAtDesc,
				prometheus.GaugeValue, timeAsUnixOrZero(maxScrapedAt),
				string(serviceType), string(serviceType),
			)
		}
		if len(plugin.ServiceInfo().Rates) > 0 {
			ch <- prometheus.MustNewConstMetric(
				minRatesScrapedAtDesc,
				prometheus.GaugeValue, timeAsUnixOrZero(minRatesScrapedAt),
				string(serviceType), string(serviceType),
			)
			ch <- prometheus.MustNewConstMetric(
				maxRatesScrapedAtDesc,
				prometheus.GaugeValue, timeAsUnixOrZero(maxRatesScrapedAt),
				string(serviceType), string(serviceType),
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
	// When .Override is set, the DB is bypassed and only the given
	// CapacityPluginMetricsInstances are considered. This is used for testing only.
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
		liquidDescribeMetrics(ch, plugin.ServiceInfo().CapacityMetricFamilies, nil)
	}
}

var capacitySerializedMetricsGetQuery = sqlext.SimplifyWhitespace(`
	SELECT capacitor_id, serialized_metrics
	  FROM cluster_capacitors
	 WHERE serialized_metrics != '' AND serialized_metrics != '{}'
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
	err := liquidCollectMetrics(ch, []byte(instance.SerializedMetrics), plugin.ServiceInfo().CapacityMetricFamilies, nil, nil)
	successAsFloat := 1.0
	if err != nil {
		successAsFloat = 0.0
		// errors in plugin.LiquidCollectMetrics() are not fatal: we record a failure in
		// the metrics and keep going with the other project services
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
	[]string{"domain", "domain_id", "project", "project_id", "service", "service_name"},
)

// QuotaPluginMetricsCollector is a prometheus.Collector that submits metrics
// which are specific to the selected quota plugins.
type QuotaPluginMetricsCollector struct {
	Cluster *core.Cluster
	DB      *gorp.DbMap
	// When .Override is set, the DB is bypassed and only the given
	// QuotaPluginMetricsInstances are considered. This is used for testing only.
	Override []QuotaPluginMetricsInstance
}

// QuotaPluginMetricsInstance describes a single project service for which plugin
// metrics are submitted. It appears in type QuotaPluginMetricsCollector.
type QuotaPluginMetricsInstance struct {
	Project           core.KeystoneProject
	ServiceType       db.ServiceType
	SerializedMetrics string
}

// Describe implements the prometheus.Collector interface.
func (c *QuotaPluginMetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	quotaPluginMetricsOkGauge.Describe(ch)
	for _, plugin := range c.Cluster.QuotaPlugins {
		liquidDescribeMetrics(ch, plugin.ServiceInfo().UsageMetricFamilies, []string{"domain_id", "project_id"})
	}
}

var quotaSerializedMetricsGetQuery = sqlext.SimplifyWhitespace(`
	SELECT d.name, d.uuid, p.name, p.uuid, p.parent_uuid, ps.type, ps.serialized_metrics
	  FROM domains d
	  JOIN projects p ON p.domain_id = d.id
	  JOIN project_services ps ON ps.project_id = p.id
	 WHERE ps.serialized_metrics != '' AND ps.serialized_metrics != '{}'
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

	err := liquidCollectMetrics(ch, []byte(instance.SerializedMetrics), plugin.ServiceInfo().UsageMetricFamilies,
		[]string{"domain_id", "project_id"},
		[]string{instance.Project.Domain.UUID, instance.Project.UUID},
	)
	successAsFloat := 1.0
	if err != nil {
		successAsFloat = 0.0
		// errors in plugin.LiquidCollectMetrics() are not fatal: we record a failure in
		// the metrics and keep going with the other project services
		logg.Error("while collecting plugin metrics for service %s in project %s: %s",
			instance.ServiceType, instance.Project.UUID, err.Error())
	}
	ch <- prometheus.MustNewConstMetric(
		pluginMetricsOkDesc,
		prometheus.GaugeValue, successAsFloat,
		instance.Project.Domain.Name, instance.Project.Domain.UUID, instance.Project.Name, instance.Project.UUID,
		string(instance.ServiceType), string(instance.ServiceType),
	)
}

////////////////////////////////////////////////////////////////////////////////
// data metrics

// DataMetricsReporter renders Prometheus metrics for data attributes (quota,
// usage, etc.) for all projects known to Limes.
//
// It is an http.Handler, instead of implementing the prometheus.Collector
// interface (like all the other Collector types in this package) and going
// through the normal promhttp facility.
//
// We are not going through promhttp here because promhttp insists on holding
// all metrics in memory before rendering them out (in order to sort them).
// Given the extremely high cardinality of these metrics, this results in
// unreasonably high memory usage spikes.
//
// This implementation also holds all the metrics in memory (because ORDER BY
// on database level turned out to be prohibitively expensive), but we hold
// their rendered forms (i.e. something like `{bar="bar",foo="foo"} 42` instead
// of a dozen allocations for each label name, label value, label pair, a map
// of label pairs, and so on) in order to save memory.
type DataMetricsReporter struct {
	Cluster      *core.Cluster
	DB           *gorp.DbMap
	ReportZeroes bool
}

// This is the same Content-Type that promhttp's GET /metrics implementation reports.
// If this changes because of a prometheus/client-go upgrade, we will know because our
// test verifies that promhttp yields this Content-Type. In the case of a change,
// the output format of promhttp should be carefully reviewed for changes, and then
// our implementation should match those changes (including to the Content-Type).
const contentTypeForPrometheusMetrics = "text/plain; version=0.0.4; charset=utf-8; escaping=underscores"

// ServeHTTP implements the http.Handler interface.
func (d *DataMetricsReporter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	metricsBySeries, err := d.collectMetricsBySeries()
	if respondwith.ErrorText(w, err) {
		return
	}

	w.Header().Set("Content-Type", contentTypeForPrometheusMetrics)
	w.WriteHeader(http.StatusOK)

	// NOTE: Keep metrics ordered by name!
	bw := bufio.NewWriter(w)
	printDataMetrics(bw, metricsBySeries, "limes_autogrow_growth_multiplier", `For resources with quota distribution model "autogrow", reports the configured growth multiplier.`)
	printDataMetrics(bw, metricsBySeries, "limes_autogrow_quota_overcommit_threshold_percent", `For resources with quota distribution model "autogrow", reports the allocation percentage above which quota overcommit is disabled.`)
	printDataMetrics(bw, metricsBySeries, "limes_available_commitment_duration", `Reports which commitment durations are available for new commitments on a Limes resource.`)
	printDataMetrics(bw, metricsBySeries, "limes_cluster_capacity", `Reported capacity of a Limes resource for an OpenStack cluster.`)
	printDataMetrics(bw, metricsBySeries, "limes_cluster_capacity_per_az", "Reported capacity of a Limes resource for an OpenStack cluster in a specific availability zone.")
	printDataMetrics(bw, metricsBySeries, "limes_cluster_usage_per_az", "Actual usage of a Limes resource for an OpenStack cluster in a specific availability zone.")
	printDataMetrics(bw, metricsBySeries, "limes_domain_quota", `Assigned quota of a Limes resource for an OpenStack domain.`)
	printDataMetrics(bw, metricsBySeries, "limes_project_backendquota", `Actual quota of a Limes resource for an OpenStack project.`)
	printDataMetrics(bw, metricsBySeries, "limes_project_commitment_min_expires_at", `Minimum expiredAt timestamp of all commitments for an Openstack project, grouped by resource and service.`)
	printDataMetrics(bw, metricsBySeries, "limes_project_committed_per_az", `Sum of all active commitments of a Limes resource for an OpenStack project, grouped by availability zone and state.`)
	printDataMetrics(bw, metricsBySeries, "limes_project_override_quota_from_config", `Quota override for a Limes resource for an OpenStack project, if any. (Value comes from cluster configuration.)`)
	printDataMetrics(bw, metricsBySeries, "limes_project_physical_usage", `Actual (physical) usage of a Limes resource for an OpenStack project.`)
	printDataMetrics(bw, metricsBySeries, "limes_project_quota", `Assigned quota of a Limes resource for an OpenStack project.`)
	printDataMetrics(bw, metricsBySeries, "limes_project_rate_usage", `Usage of a Limes rate for an OpenStack project. These are counters that never reset.`)
	printDataMetrics(bw, metricsBySeries, "limes_project_usage", `Actual (logical) usage of a Limes resource for an OpenStack project.`)
	printDataMetrics(bw, metricsBySeries, "limes_project_usage_per_az", `Actual (logical) usage of a Limes resource for an OpenStack project in a specific availability zone.`)
	printDataMetrics(bw, metricsBySeries, "limes_project_used_and_or_committed_per_az", `The maximum of limes_project_usage_per_az and limes_project_committed_per_az{state="active"}.`)
	printDataMetrics(bw, metricsBySeries, "limes_unit_multiplier", `Conversion factor that a value of this resource must be multiplied with to obtain the base unit (e.g. bytes). For use with Grafana when only the base unit can be configured because of templating.`)

	err = bw.Flush()
	if err != nil {
		logg.Error("in DataMetricsReporter.ServeHTTP: " + err.Error())
	}
}

type dataMetric struct {
	Labels string // e.g. `bar="bar",foo="foo"`
	Value  float64
}

func printDataMetrics(w io.Writer, metricsBySeries map[string][]dataMetric, seriesName, seriesHelp string) {
	metrics := metricsBySeries[seriesName]
	if len(metrics) == 0 {
		return
	}
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n", seriesName, seriesHelp, seriesName)

	slices.SortFunc(metrics, func(lhs, rhs dataMetric) int {
		return strings.Compare(lhs.Labels, rhs.Labels)
	})
	for _, m := range metrics {
		fmt.Fprintf(w, "%s{%s} %g\n", seriesName, m.Labels, m.Value)
	}
}

var clusterMetricsQuery = sqlext.SimplifyWhitespace(`
	SELECT cs.type, cr.name, JSON_OBJECT_AGG(car.az, car.raw_capacity), JSON_OBJECT_AGG(car.az, car.usage)
	  FROM cluster_services cs
	  JOIN cluster_resources cr ON cr.service_id = cs.id
	  JOIN cluster_az_resources car ON car.resource_id = cr.id
	 GROUP BY cs.type, cr.name
`)

var domainMetricsQuery = sqlext.SimplifyWhitespace(`
	SELECT d.name, d.uuid, ps.type, pr.name, SUM(pr.quota)
	  FROM domains d
	  JOIN projects p ON p.domain_id = d.id
	  JOIN project_services ps ON ps.project_id = p.id
	  JOIN project_resources pr ON pr.service_id = ps.id
	 GROUP BY d.name, d.uuid, ps.type, pr.name
`)

var projectMetricsQuery = sqlext.SimplifyWhitespace(`
	WITH project_sums AS (
	  SELECT resource_id,
	         SUM(usage) AS usage,
	         SUM(COALESCE(physical_usage, usage)) AS physical_usage,
	         COUNT(physical_usage) > 0 AS has_physical_usage
	    FROM project_az_resources
	   GROUP BY resource_id
	),
	project_commitment_minExpiresAt AS (
		SELECT p.domain_id, p.id AS project_id, ps.type, pr.name, MIN(expires_at) AS project_commitment_min_expires_at
		FROM projects p
		JOIN project_services ps ON ps.project_id = p.id
		JOIN project_resources pr ON pr.service_id = ps.id
		JOIN project_az_resources par ON par.resource_id = pr.id
		JOIN project_commitments pc ON pc.az_resource_id = par.id AND pc.state = 'active'
		GROUP BY p.domain_id, p.id, ps.type, pr.name 
	)
	SELECT d.name, d.uuid, p.name, p.uuid, ps.type, pr.name,
	       pr.quota, pr.backend_quota, pr.override_quota_from_config,
	       psums.usage, psums.physical_usage, psums.has_physical_usage,
	       pcmea.project_commitment_min_expires_at
	  FROM domains d
	  JOIN projects p ON p.domain_id = d.id
	  JOIN project_services ps ON ps.project_id = p.id
	  JOIN project_resources pr ON pr.service_id = ps.id
	  JOIN project_sums psums ON psums.resource_id = pr.id
	  LEFT JOIN project_commitment_minExpiresAt pcmea ON d.id = pcmea.domain_id AND p.id = pcmea.project_id AND ps.type= pcmea.TYPE AND pr.name = pcmea.name
`)

var projectAZMetricsQuery = sqlext.SimplifyWhitespace(`
	WITH project_commitment_sums_by_state AS (
	  SELECT az_resource_id, state, SUM(amount) AS amount
	    FROM project_commitments
	   WHERE state NOT IN ('superseded', 'expired')
	   GROUP BY az_resource_id, state
	), project_commitment_sums AS (
	  SELECT az_resource_id, JSON_OBJECT_AGG(state, amount) AS amount_by_state
	    FROM project_commitment_sums_by_state
	   GROUP BY az_resource_id
	)
	SELECT d.name, d.uuid, p.name, p.uuid, ps.type, pr.name, par.az, par.usage, pcs.amount_by_state
	  FROM domains d
	  JOIN projects p ON p.domain_id = d.id
	  JOIN project_services ps ON ps.project_id = p.id
	  JOIN project_resources pr ON pr.service_id = ps.id
	  JOIN project_az_resources par ON par.resource_id = pr.id
	  LEFT OUTER JOIN project_commitment_sums pcs ON pcs.az_resource_id = par.id
`)

var projectRateMetricsQuery = sqlext.SimplifyWhitespace(`
	SELECT d.name, d.uuid, p.name, p.uuid, ps.type, pra.name, pra.usage_as_bigint
	  FROM domains d
	  JOIN projects p ON p.domain_id = d.id
	  JOIN project_services ps ON ps.project_id = p.id
	  JOIN project_rates pra ON pra.service_id = ps.id
	 WHERE pra.usage_as_bigint != ''
`)

func (d *DataMetricsReporter) collectMetricsBySeries() (map[string][]dataMetric, error) {
	behaviorCache := newResourceAndRateBehaviorCache(d.Cluster)
	result := make(map[string][]dataMetric)

	// fetch values for cluster level
	capacityReported := make(map[db.ServiceType]map[liquid.ResourceName]bool)
	err := sqlext.ForeachRow(d.DB, clusterMetricsQuery, nil, func(rows *sql.Rows) error {
		var (
			dbServiceType     db.ServiceType
			dbResourceName    liquid.ResourceName
			capacityPerAZJSON string
			usagePerAZJSON    string
		)
		err := rows.Scan(&dbServiceType, &dbResourceName, &capacityPerAZJSON, &usagePerAZJSON)
		if err != nil {
			return err
		}

		var (
			capacityPerAZ map[liquid.AvailabilityZone]uint64
			usagePerAZ    map[liquid.AvailabilityZone]*uint64
		)
		err = json.Unmarshal([]byte(capacityPerAZJSON), &capacityPerAZ)
		if err != nil {
			return err
		}
		err = json.Unmarshal([]byte(usagePerAZJSON), &usagePerAZ)
		if err != nil {
			return err
		}
		reportAZBreakdown := false
		totalCapacity := uint64(0)
		for az, azCapacity := range capacityPerAZ {
			totalCapacity += azCapacity
			if az != liquid.AvailabilityZoneAny {
				reportAZBreakdown = true
			}
		}

		behavior := behaviorCache.Get(dbServiceType, dbResourceName)
		apiIdentity := behavior.IdentityInV1API
		if reportAZBreakdown {
			for az, azCapacity := range capacityPerAZ {
				azLabels := fmt.Sprintf(`availability_zone=%q,resource=%q,service=%q,service_name=%q`,
					az, apiIdentity.Name, apiIdentity.ServiceType, dbServiceType,
				)
				metric := dataMetric{Labels: azLabels, Value: float64(behavior.OvercommitFactor.ApplyTo(azCapacity))}
				result["limes_cluster_capacity_per_az"] = append(result["limes_cluster_capacity_per_az"], metric)

				azUsage := usagePerAZ[az]
				if azUsage != nil && *azUsage != 0 {
					metric := dataMetric{Labels: azLabels, Value: float64(*azUsage)}
					result["limes_cluster_usage_per_az"] = append(result["limes_cluster_usage_per_az"], metric)
				}
			}
		}

		labels := fmt.Sprintf(`resource=%q,service=%q,service_name=%q`,
			apiIdentity.Name, apiIdentity.ServiceType, dbServiceType,
		)
		metric := dataMetric{Labels: labels, Value: float64(behavior.OvercommitFactor.ApplyTo(totalCapacity))}
		result["limes_cluster_capacity"] = append(result["limes_cluster_capacity"], metric)

		_, exists := capacityReported[dbServiceType]
		if !exists {
			capacityReported[dbServiceType] = make(map[liquid.ResourceName]bool)
		}
		capacityReported[dbServiceType][dbResourceName] = true

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("in clusterMetricsQuery: %w", err)
	}

	// make sure that a cluster capacity value is reported for each resource (the
	// corresponding time series might otherwise be missing if capacity scraping
	// fails)
	for serviceType, quotaPlugin := range d.Cluster.QuotaPlugins {
		for resName := range quotaPlugin.ServiceInfo().Resources {
			if capacityReported[serviceType][resName] {
				continue
			}
			apiIdentity := behaviorCache.Get(serviceType, resName).IdentityInV1API

			labels := fmt.Sprintf(`resource=%q,service=%q,service_name=%q`,
				apiIdentity.Name, apiIdentity.ServiceType, serviceType,
			)
			metric := dataMetric{Labels: labels, Value: 0}
			result["limes_cluster_capacity"] = append(result["limes_cluster_capacity"], metric)
		}
	}

	// fetch values for domain level
	err = sqlext.ForeachRow(d.DB, domainMetricsQuery, nil, func(rows *sql.Rows) error {
		var (
			domainName     string
			domainUUID     string
			dbServiceType  db.ServiceType
			dbResourceName liquid.ResourceName
			quota          *uint64
		)
		err := rows.Scan(&domainName, &domainUUID, &dbServiceType, &dbResourceName, &quota)
		if err != nil {
			return err
		}
		apiIdentity := behaviorCache.Get(dbServiceType, dbResourceName).IdentityInV1API

		if quota != nil {
			labels := fmt.Sprintf(
				`domain=%q,domain_id=%q,resource=%q,service=%q,service_name=%q`,
				domainName, domainUUID,
				apiIdentity.Name, apiIdentity.ServiceType, dbServiceType,
			)
			metric := dataMetric{Labels: labels, Value: float64(*quota)}
			result["limes_domain_quota"] = append(result["limes_domain_quota"], metric)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("during domainMetricsQuery: %w", err)
	}

	// fetch values for project level (quota/usage)
	err = sqlext.ForeachRow(d.DB, projectMetricsQuery, nil, func(rows *sql.Rows) error {
		var (
			domainName              string
			domainUUID              string
			projectName             string
			projectUUID             string
			dbServiceType           db.ServiceType
			dbResourceName          liquid.ResourceName
			quota                   *uint64
			backendQuota            *int64
			overrideQuotaFromConfig *uint64
			usage                   uint64
			physicalUsage           uint64
			hasPhysicalUsage        bool
			minExpiresAt            *time.Time
		)
		err := rows.Scan(&domainName, &domainUUID, &projectName, &projectUUID, &dbServiceType, &dbResourceName,
			&quota, &backendQuota, &overrideQuotaFromConfig, &usage, &physicalUsage, &hasPhysicalUsage, &minExpiresAt)
		if err != nil {
			return err
		}
		apiIdentity := behaviorCache.Get(dbServiceType, dbResourceName).IdentityInV1API

		labels := fmt.Sprintf(
			`domain=%q,domain_id=%q,project=%q,project_id=%q,resource=%q,service=%q,service_name=%q`,
			domainName, domainUUID, projectName, projectUUID,
			apiIdentity.Name, apiIdentity.ServiceType, dbServiceType,
		)

		if quota != nil {
			if d.ReportZeroes || *quota != 0 {
				metric := dataMetric{Labels: labels, Value: float64(*quota)}
				result["limes_project_quota"] = append(result["limes_project_quota"], metric)
			}
		}
		if backendQuota != nil {
			if d.ReportZeroes || *backendQuota != 0 {
				metric := dataMetric{Labels: labels, Value: float64(*backendQuota)}
				result["limes_project_backendquota"] = append(result["limes_project_backendquota"], metric)
			}
		}
		if overrideQuotaFromConfig != nil {
			metric := dataMetric{Labels: labels, Value: float64(*overrideQuotaFromConfig)}
			result["limes_project_override_quota_from_config"] = append(result["limes_project_override_quota_from_config"], metric)
		}
		if d.ReportZeroes || usage != 0 {
			metric := dataMetric{Labels: labels, Value: float64(usage)}
			result["limes_project_usage"] = append(result["limes_project_usage"], metric)
		}
		if hasPhysicalUsage {
			if d.ReportZeroes || physicalUsage != 0 {
				metric := dataMetric{Labels: labels, Value: float64(physicalUsage)}
				result["limes_project_physical_usage"] = append(result["limes_project_physical_usage"], metric)
			}
		}
		if minExpiresAt != nil || d.ReportZeroes {
			metric := dataMetric{Labels: labels, Value: timeAsUnixOrZero(minExpiresAt)}
			result["limes_project_commitment_min_expires_at"] = append(result["limes_project_commitment_min_expires_at"], metric)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("during projectMetricsQuery: %w", err)
	}

	// fetch values for project AZ level (usage/commitments)
	err = sqlext.ForeachRow(d.DB, projectAZMetricsQuery, nil, func(rows *sql.Rows) error {
		var (
			domainName        string
			domainUUID        string
			projectName       string
			projectUUID       string
			dbServiceType     db.ServiceType
			dbResourceName    liquid.ResourceName
			az                liquid.AvailabilityZone
			usage             uint64
			amountByStateJSON *string
		)
		err := rows.Scan(&domainName, &domainUUID, &projectName, &projectUUID, &dbServiceType, &dbResourceName,
			&az, &usage, &amountByStateJSON)
		if err != nil {
			return err
		}
		apiIdentity := behaviorCache.Get(dbServiceType, dbResourceName).IdentityInV1API

		labels := fmt.Sprintf(
			`availability_zone=%q,domain=%q,domain_id=%q,project=%q,project_id=%q,resource=%q,service=%q,service_name=%q`,
			az, domainName, domainUUID, projectName, projectUUID,
			apiIdentity.Name, apiIdentity.ServiceType, dbServiceType,
		)

		if d.ReportZeroes || usage != 0 {
			metric := dataMetric{Labels: labels, Value: float64(usage)}
			result["limes_project_usage_per_az"] = append(result["limes_project_usage_per_az"], metric)
		}
		committed := uint64(0)
		if amountByStateJSON != nil {
			var amountByState map[db.CommitmentState]uint64
			err = json.Unmarshal([]byte(*amountByStateJSON), &amountByState)
			if err != nil {
				return fmt.Errorf("while unmarshalling amount_by_state: %w (input was %q)", err, *amountByStateJSON)
			}
			committed = amountByState[db.CommitmentStateActive]
			for state, amount := range amountByState {
				labelsWithState := fmt.Sprintf(`%s,state=%q`, labels, state)
				metric := dataMetric{Labels: labelsWithState, Value: float64(amount)}
				result["limes_project_committed_per_az"] = append(result["limes_project_committed_per_az"], metric)
			}
		}
		if d.ReportZeroes || max(usage, committed) != 0 {
			metric := dataMetric{Labels: labels, Value: float64(max(usage, committed))}
			result["limes_project_used_and_or_committed_per_az"] = append(result["limes_project_used_and_or_committed_per_az"], metric)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("during projectAZMetricsQuery: %w", err)
	}

	// fetch metadata for services/resources
	for dbServiceType, quotaPlugin := range d.Cluster.QuotaPlugins {
		for dbResourceName, resourceInfo := range quotaPlugin.ServiceInfo().Resources {
			behavior := behaviorCache.Get(dbServiceType, dbResourceName)
			apiIdentity := behavior.IdentityInV1API
			labels := fmt.Sprintf(`resource=%q,service=%q,service_name=%q`,
				apiIdentity.Name, apiIdentity.ServiceType, dbServiceType,
			)

			_, multiplier := resourceInfo.Unit.Base()
			metric := dataMetric{Labels: labels, Value: float64(multiplier)}
			result["limes_unit_multiplier"] = append(result["limes_unit_multiplier"], metric)

			autogrowCfg, ok := d.Cluster.QuotaDistributionConfigForResource(dbServiceType, dbResourceName).Autogrow.Unpack()
			if ok {
				metric := dataMetric{Labels: labels, Value: autogrowCfg.GrowthMultiplier}
				result["limes_autogrow_growth_multiplier"] = append(result["limes_autogrow_growth_multiplier"], metric)

				metric = dataMetric{Labels: labels, Value: autogrowCfg.AllowQuotaOvercommitUntilAllocatedPercent}
				result["limes_autogrow_quota_overcommit_threshold_percent"] = append(result["limes_autogrow_quota_overcommit_threshold_percent"], metric)
			}

			for _, duration := range behaviorCache.GetCommitmentBehavior(dbServiceType, dbResourceName).Durations {
				labels := fmt.Sprintf(`duration=%q,resource=%q,service=%q,service_name=%q`,
					duration.String(), apiIdentity.Name, apiIdentity.ServiceType, dbServiceType,
				)
				metric := dataMetric{Labels: labels, Value: 1.0}
				result["limes_available_commitment_duration"] = append(result["limes_available_commitment_duration"], metric)
			}
		}
	}

	// fetch values for project level (rate usage)
	err = sqlext.ForeachRow(d.DB, projectRateMetricsQuery, nil, func(rows *sql.Rows) error {
		var (
			domainName    string
			domainUUID    string
			projectName   string
			projectUUID   string
			dbServiceType db.ServiceType
			dbRateName    liquid.RateName
			usageAsBigint string
		)
		err := rows.Scan(&domainName, &domainUUID, &projectName, &projectUUID, &dbServiceType, &dbRateName, &usageAsBigint)
		if err != nil {
			return err
		}
		usageAsBigFloat, _, err := big.NewFloat(0).Parse(usageAsBigint, 10)
		if err != nil {
			return err
		}
		usageAsFloat, _ := usageAsBigFloat.Float64()

		if d.ReportZeroes || usageAsFloat != 0 {
			behavior := behaviorCache.GetForRate(dbServiceType, dbRateName)
			apiIdentity := behavior.IdentityInV1API
			labels := fmt.Sprintf(
				`domain=%q,domain_id=%q,project=%q,project_id=%q,rate=%q,service=%q,service_name=%q`,
				domainName, domainUUID, projectName, projectUUID,
				apiIdentity.Name, apiIdentity.ServiceType, dbServiceType,
			)
			metric := dataMetric{Labels: labels, Value: usageAsFloat}
			result["limes_project_rate_usage"] = append(result["limes_project_rate_usage"], metric)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("during projectRateMetricsQuery: %w", err)
	}

	return result, nil
}

///////////////////////////////////////////////////////////////////////////////////////////
// utilities

// Caches the result of repeated cluster.BehaviorForResource() calls.
//
// NOTE: This looks like something that should be baked into BehaviorForResource() itself.
// But then cache access would need to be protected by a mutex, which would likely negate the performance gain from caching.
// We could revisit the idea of more central caching once <https://github.com/golang/go/issues/71076> makes thread-safe maps more viable.
//
// Alternatively, once ServiceInfo and ResourceInfo gets refactored towards being stored in the DB,
// we could consider persisting behavior information there as well. But this might introduce additional
// complications to account for behaviors being updated without the underlying ResourceInfo changing.
type resourceAndRateBehaviorCache struct {
	cluster   *core.Cluster
	cache     map[db.ServiceType]map[liquid.ResourceName]core.ResourceBehavior
	rateCache map[db.ServiceType]map[liquid.RateName]core.RateBehavior
	cbCache   map[db.ServiceType]map[liquid.ResourceName]core.ScopedCommitmentBehavior
}

func newResourceAndRateBehaviorCache(cluster *core.Cluster) resourceAndRateBehaviorCache {
	cache := make(map[db.ServiceType]map[liquid.ResourceName]core.ResourceBehavior)
	rateCache := make(map[db.ServiceType]map[liquid.RateName]core.RateBehavior)
	cbCache := make(map[db.ServiceType]map[liquid.ResourceName]core.ScopedCommitmentBehavior)
	return resourceAndRateBehaviorCache{cluster, cache, rateCache, cbCache}
}

func (c resourceAndRateBehaviorCache) Get(srvType db.ServiceType, resName liquid.ResourceName) core.ResourceBehavior {
	if c.cache[srvType] == nil {
		c.cache[srvType] = make(map[liquid.ResourceName]core.ResourceBehavior)
	}
	behavior, exists := c.cache[srvType][resName]
	if !exists {
		behavior = c.cluster.BehaviorForResource(srvType, resName)
		c.cache[srvType][resName] = behavior
	}
	return behavior
}

func (c resourceAndRateBehaviorCache) GetForRate(srvType db.ServiceType, rateName liquid.RateName) core.RateBehavior {
	if c.rateCache[srvType] == nil {
		c.rateCache[srvType] = make(map[liquid.RateName]core.RateBehavior)
	}
	behavior, exists := c.rateCache[srvType][rateName]
	if !exists {
		behavior = c.cluster.BehaviorForRate(srvType, rateName)
		c.rateCache[srvType][rateName] = behavior
	}
	return behavior
}

func (c resourceAndRateBehaviorCache) GetCommitmentBehavior(srvType db.ServiceType, resName liquid.ResourceName) core.ScopedCommitmentBehavior {
	if c.cbCache[srvType] == nil {
		c.cbCache[srvType] = make(map[liquid.ResourceName]core.ScopedCommitmentBehavior)
	}
	behavior, exists := c.cbCache[srvType][resName]
	if !exists {
		behavior = c.cluster.CommitmentBehaviorForResource(srvType, resName).ForCluster()
		c.cbCache[srvType][resName] = behavior
	}
	return behavior
}
