// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"maps"
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

var minScrapedAtGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_oldest_scraped_at",
		Help: "Oldest (i.e. smallest) scraped_at timestamp for any project given a certain service in a certain OpenStack cluster.",
	},
	[]string{"service"},
)

var maxScrapedAtGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_newest_scraped_at",
		Help: "Newest (i.e. largest) scraped_at timestamp for any project given a certain service in a certain OpenStack cluster.",
	},
	[]string{"service"},
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
}

var scrapedAtAggregateQuery = sqlext.SimplifyWhitespace(`
	SELECT s.type, MIN(ps.scraped_at), MAX(ps.scraped_at), MIN(ps.scraped_at), MAX(ps.scraped_at)
	  FROM project_services ps
	  JOIN services s ON s.id = ps.service_id
	 WHERE ps.scraped_at IS NOT NULL
	 GROUP BY s.type
`)

// Collect implements the prometheus.Collector interface.
func (c *AggregateMetricsCollector) Collect(ch chan<- prometheus.Metric) {
	// NOTE: I use NewConstMetric() instead of storing the values in the GaugeVec
	// instances because it is faster.

	descCh := make(chan *prometheus.Desc, 1)
	minScrapedAtGauge.Describe(descCh)
	minScrapedAtDesc := <-descCh
	maxScrapedAtGauge.Describe(descCh)
	maxScrapedAtDesc := <-descCh

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

		connection := c.Cluster.LiquidConnections[serviceType]
		if connection == nil {
			return nil
		}

		if len(connection.ServiceInfo().Resources) > 0 {
			ch <- prometheus.MustNewConstMetric(
				minScrapedAtDesc,
				prometheus.GaugeValue, timeAsUnixOrZero(minScrapedAt),
				string(serviceType),
			)
			ch <- prometheus.MustNewConstMetric(
				maxScrapedAtDesc,
				prometheus.GaugeValue, timeAsUnixOrZero(maxScrapedAt),
				string(serviceType),
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
// capacity collection metrics

var capacityCollectionMetricsOkGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_capacity_collection_metrics_ok",
		Help: "Whether capacity collection metrics were rendered successfully for a particular service_type. Only present when the service_type emits metrics.",
	},
	[]string{"service_type"},
)

// CapacityCollectionMetricsCollector is a prometheus.Collector that submits metrics
type CapacityCollectionMetricsCollector struct {
	Cluster *core.Cluster
	DB      *gorp.DbMap
	// When .Override is set, the DB is bypassed and only the given
	// CapacityCollectionMetricsInstances are considered. This is used for testing only.
	Override []CapacityCollectionMetricsInstance
}

// CapacityCollectionMetricsInstance describes a single project service for which collection
// metrics are submitted. It appears in type CapacityCollectionMetricsCollector.
type CapacityCollectionMetricsInstance struct {
	ServiceType       db.ServiceType
	SerializedMetrics string
}

// Describe implements the prometheus.Collector interface.
func (c *CapacityCollectionMetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	capacityCollectionMetricsOkGauge.Describe(ch)
	for _, connection := range c.Cluster.LiquidConnections {
		liquidDescribeMetrics(ch, connection.ServiceInfo().CapacityMetricFamilies, nil)
	}
}

var capacitySerializedMetricsGetQuery = sqlext.SimplifyWhitespace(`
	SELECT type, serialized_metrics
	  FROM services
	 WHERE serialized_metrics != '' AND serialized_metrics != '{}'
`)

// Collect implements the prometheus.Collector interface.
func (c *CapacityCollectionMetricsCollector) Collect(ch chan<- prometheus.Metric) {
	descCh := make(chan *prometheus.Desc, 1)
	capacityCollectionMetricsOkGauge.Describe(descCh)
	collectionMetricsOkDesc := <-descCh

	if c.Override != nil {
		for _, instance := range c.Override {
			c.collectOneCapacitor(ch, collectionMetricsOkDesc, instance)
		}
		return
	}

	err := sqlext.ForeachRow(c.DB, capacitySerializedMetricsGetQuery, nil, func(rows *sql.Rows) error {
		var i CapacityCollectionMetricsInstance
		err := rows.Scan(&i.ServiceType, &i.SerializedMetrics)
		if err == nil {
			c.collectOneCapacitor(ch, collectionMetricsOkDesc, i)
		}
		return err
	})
	if err != nil {
		logg.Error("collect capacity collection metrics failed: " + err.Error())
	}
}

func (c *CapacityCollectionMetricsCollector) collectOneCapacitor(ch chan<- prometheus.Metric, collectionMetricsOkDesc *prometheus.Desc, instance CapacityCollectionMetricsInstance) {
	connection := c.Cluster.LiquidConnections[instance.ServiceType]
	if connection == nil {
		return
	}
	err := liquidCollectMetrics(ch, []byte(instance.SerializedMetrics), connection.ServiceInfo().CapacityMetricFamilies, nil, nil)
	successAsFloat := 1.0
	if err != nil {
		successAsFloat = 0.0
		// errors in connection.LiquidCollectMetrics() are not fatal: we record a failure in
		// the metrics and keep going with the other project services
		logg.Error("while collecting capacity metrics for service_type %s: %s",
			instance.ServiceType, err.Error())
	}
	ch <- prometheus.MustNewConstMetric(
		collectionMetricsOkDesc,
		prometheus.GaugeValue, successAsFloat,
		string(instance.ServiceType),
	)
}

////////////////////////////////////////////////////////////////////////////////
// usage collection metrics

var usageCollectionMetricsOkGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_usage_collection_metrics_ok",
		Help: "Whether usage collection metrics were rendered successfully for a particular project service. Only present when the project service emits metrics.",
	},
	[]string{"domain", "domain_id", "project", "project_id", "service", "service_name"},
)

// UsageCollectionMetricsCollector is a prometheus.Collector that submits metrics
type UsageCollectionMetricsCollector struct {
	Cluster *core.Cluster
	DB      *gorp.DbMap
	// When .Override is set, the DB is bypassed and only the given
	// QuotaCollectionMetricsInstances are considered. This is used for testing only.
	Override []QuotaCollectionMetricsInstance
}

// QuotaCollectionMetricsInstance describes a single project service for which collection
// metrics are submitted. It appears in type UsageCollectionMetricsCollector.
type QuotaCollectionMetricsInstance struct {
	Project           core.KeystoneProject
	ServiceType       db.ServiceType
	SerializedMetrics string
}

// Describe implements the prometheus.Collector interface.
func (c *UsageCollectionMetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	usageCollectionMetricsOkGauge.Describe(ch)
	for _, connection := range c.Cluster.LiquidConnections {
		liquidDescribeMetrics(ch, connection.ServiceInfo().UsageMetricFamilies, []string{"domain_id", "project_id"})
	}
}

var quotaSerializedMetricsGetQuery = sqlext.SimplifyWhitespace(`
	SELECT d.name, d.uuid, p.name, p.uuid, p.parent_uuid, s.type, ps.serialized_metrics
	  FROM services s
	  CROSS JOIN domains d
	  JOIN projects p ON p.domain_id = d.id
	  JOIN project_services ps ON ps.service_id = s.id AND ps.project_id = p.id
	 WHERE ps.serialized_metrics != '' AND ps.serialized_metrics != '{}'
`)

// Collect implements the prometheus.Collector interface.
func (c *UsageCollectionMetricsCollector) Collect(ch chan<- prometheus.Metric) {
	descCh := make(chan *prometheus.Desc, 1)
	usageCollectionMetricsOkGauge.Describe(descCh)
	collectionMetricsOkDesc := <-descCh

	if c.Override != nil {
		for _, instance := range c.Override {
			c.collectOneProjectService(ch, collectionMetricsOkDesc, instance)
		}
		return
	}

	err := sqlext.ForeachRow(c.DB, quotaSerializedMetricsGetQuery, nil, func(rows *sql.Rows) error {
		var i QuotaCollectionMetricsInstance
		err := rows.Scan(
			&i.Project.Domain.Name, &i.Project.Domain.UUID,
			&i.Project.Name, &i.Project.UUID, &i.Project.ParentUUID,
			&i.ServiceType, &i.SerializedMetrics)
		if err == nil {
			c.collectOneProjectService(ch, collectionMetricsOkDesc, i)
		}
		return err
	})
	if err != nil {
		logg.Error("collect usage collection metrics failed: " + err.Error())
	}
}

func (c *UsageCollectionMetricsCollector) collectOneProjectService(ch chan<- prometheus.Metric, collectionMetricsOkDesc *prometheus.Desc, instance QuotaCollectionMetricsInstance) {
	connection := c.Cluster.LiquidConnections[instance.ServiceType]
	if connection == nil {
		return
	}

	err := liquidCollectMetrics(ch, []byte(instance.SerializedMetrics), connection.ServiceInfo().UsageMetricFamilies,
		[]string{"domain_id", "project_id"},
		[]string{instance.Project.Domain.UUID, string(instance.Project.UUID)},
	)
	successAsFloat := 1.0
	if err != nil {
		successAsFloat = 0.0
		// errors in connection.LiquidCollectMetrics() are not fatal: we record a failure in
		// the metrics and keep going with the other project services
		logg.Error("while collecting connection metrics for service %s in project %s: %s",
			instance.ServiceType, instance.Project.UUID, err.Error())
	}
	ch <- prometheus.MustNewConstMetric(
		collectionMetricsOkDesc,
		prometheus.GaugeValue, successAsFloat,
		instance.Project.Domain.Name, instance.Project.Domain.UUID, instance.Project.Name, string(instance.Project.UUID),
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
//
// This exporter cannot use Cluster.LiquidConnections, because it runs outside
// of the collect task. Therefore, it uses the convenience methods of the Cluster
// to get the necessary liquid.ResourceInfo data.
type DataMetricsReporter struct {
	Cluster      *core.Cluster
	DB           *gorp.DbMap
	ReportZeroes bool
}

// ContentTypeForPrometheusMetrics is the same Content-Type that promhttp's GET /metrics implementation reports.
// If this changes because of a prometheus/client-go upgrade, we will know because our
// test verifies that promhttp yields this Content-Type. In the case of a change,
// the output format of promhttp should be carefully reviewed for changes, and then
// our implementation should match those changes (including to the Content-Type).
const ContentTypeForPrometheusMetrics = "text/plain; version=0.0.4; charset=utf-8; escaping=underscores"

// ServeHTTP implements the http.Handler interface.
func (d *DataMetricsReporter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	metricsBySeries, err := d.collectMetricsBySeries()
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	w.Header().Set("Content-Type", ContentTypeForPrometheusMetrics)
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

var clusterMetricsQuery = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
	SELECT s.type, r.name, JSON_OBJECT_AGG(azr.az, azr.raw_capacity), JSON_OBJECT_AGG(azr.az, azr.usage)
	  FROM services s
	  JOIN resources r ON r.service_id = s.id
	  JOIN az_resources azr ON azr.resource_id = r.id AND azr.az != {{liquid.AvailabilityZoneTotal}}
	 GROUP BY s.type, r.name
`))

var domainMetricsQuery = sqlext.SimplifyWhitespace(`
	SELECT d.name, d.uuid, s.type, r.name, SUM(pr.quota)
	  FROM services s
	  JOIN resources r ON r.service_id = s.id
	  CROSS JOIN domains d
	  JOIN projects p ON p.domain_id = d.id
	  JOIN project_services ps ON ps.project_id = p.id AND ps.service_id = s.id
	  JOIN project_resources pr ON pr.project_id = p.id AND pr.resource_id = r.id
	 GROUP BY d.name, d.uuid, s.type, r.name
`)

var projectMetricsQuery = sqlext.SimplifyWhitespace(`
	WITH project_sums AS (
	  SELECT azr.resource_id, pazr.project_id,
	         SUM(pazr.usage) AS usage,
	         SUM(COALESCE(pazr.physical_usage, pazr.usage)) AS physical_usage,
	         COUNT(pazr.physical_usage) > 0 AS has_physical_usage
	    FROM project_az_resources pazr
	    JOIN az_resources azr ON azr.id = pazr.az_resource_id
	   GROUP BY azr.resource_id, pazr.project_id
	),
	project_commitment_minExpiresAt AS (
		SELECT p.domain_id, p.id AS project_id, s.type, r.name, MIN(expires_at) AS project_commitment_min_expires_at
		FROM services s
		JOIN resources r ON r.service_id = s.id
		JOIN az_resources azr ON azr.resource_id = r.id
		JOIN project_commitments pc ON pc.az_resource_id = azr.id AND pc.status = 'confirmed'
		JOIN projects p ON p.id = pc.project_id
		GROUP BY p.domain_id, p.id, s.type, r.name
	)
	SELECT d.name, d.uuid, p.name, p.uuid, s.type, r.name,
	       pr.quota, pr.backend_quota, pr.override_quota_from_config,
	       psums.usage, psums.physical_usage, psums.has_physical_usage,
	       pcmea.project_commitment_min_expires_at
	  FROM services s
	  JOIN resources r ON r.service_id = s.id
	  CROSS JOIN domains d
	  JOIN projects p ON p.domain_id = d.id
	  JOIN project_resources pr ON pr.resource_id = r.id AND pr.project_id = p.id
	  JOIN project_sums psums ON psums.resource_id = r.id AND psums.project_id = p.id
	  LEFT JOIN project_commitment_minExpiresAt pcmea ON d.id = pcmea.domain_id AND p.id = pcmea.project_id AND s.type= pcmea.TYPE AND r.name = pcmea.name
`)

var projectAZMetricsQuery = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
	WITH project_commitment_sums_by_status AS (
	  SELECT az_resource_id, project_id, status, SUM(amount) AS amount
	    FROM project_commitments
	   WHERE status NOT IN ('superseded', 'expired')
	   GROUP BY az_resource_id, project_id, status
	), project_commitment_sums AS (
	  SELECT az_resource_id, project_id, JSON_OBJECT_AGG(status, amount) AS amount_by_status
	    FROM project_commitment_sums_by_status
	   GROUP BY az_resource_id, project_id
	)
	SELECT d.name, d.uuid, p.name, p.uuid, s.type, r.name, cazr.az, pazr.usage, pcs.amount_by_status
	  FROM services s
	  JOIN resources r ON r.service_id = s.id
	  JOIN az_resources cazr ON cazr.resource_id = r.id AND cazr.az != {{liquid.AvailabilityZoneTotal}}
	  CROSS JOIN domains d
	  JOIN projects p ON p.domain_id = d.id
	  JOIN project_az_resources pazr ON pazr.az_resource_id = cazr.id AND pazr.project_id = p.id
	  LEFT OUTER JOIN project_commitment_sums pcs ON pcs.az_resource_id = cazr.id AND pcs.project_id = p.id
`))

var projectRateMetricsQuery = sqlext.SimplifyWhitespace(`
	SELECT d.name, d.uuid, p.name, p.uuid, s.type, ra.name, pra.usage_as_bigint
	  FROM services s
	  JOIN rates ra ON ra.service_id = s.id
	  CROSS JOIN domains d
	  JOIN projects p ON p.domain_id = d.id
	  JOIN project_rates pra ON pra.rate_id = ra.id AND pra.project_id = p.id
	 WHERE pra.usage_as_bigint != ''
`)

func (d *DataMetricsReporter) collectMetricsBySeries() (map[string][]dataMetric, error) {
	behaviorCache := newResourceAndRateBehaviorCache(d.Cluster)
	serviceInfos, err := d.Cluster.AllServiceInfos()
	if err != nil {
		return nil, err
	}
	result := make(map[string][]dataMetric)

	// fetch values for cluster level
	capacityReported := make(map[db.ServiceType]map[liquid.ResourceName]bool)
	err = sqlext.ForeachRow(d.DB, clusterMetricsQuery, nil, func(rows *sql.Rows) error {
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
				if slices.Contains([]liquid.AvailabilityZone{liquid.AvailabilityZoneAny, liquid.AvailabilityZoneUnknown}, az) && azCapacity == 0 {
					// Skip "unknown" + "any" AZs with zero capacity.
					// We have them in the DB for completeness, but the metrics are of no use in this case.
					continue
				}
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
	for _, serviceType := range slices.Sorted(maps.Keys(serviceInfos)) {
		for resName := range serviceInfos[serviceType].Resources {
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
			domainName         string
			domainUUID         string
			projectName        string
			projectUUID        string
			dbServiceType      db.ServiceType
			dbResourceName     liquid.ResourceName
			az                 liquid.AvailabilityZone
			usage              uint64
			amountByStatusJSON *string
		)
		err := rows.Scan(&domainName, &domainUUID, &projectName, &projectUUID, &dbServiceType, &dbResourceName,
			&az, &usage, &amountByStatusJSON)
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
		if amountByStatusJSON != nil {
			var amountByStatus map[liquid.CommitmentStatus]uint64
			err = json.Unmarshal([]byte(*amountByStatusJSON), &amountByStatus)
			if err != nil {
				return fmt.Errorf("while unmarshalling amount_by_status: %w (input was %q)", err, *amountByStatusJSON)
			}
			committed = amountByStatus[liquid.CommitmentStatusConfirmed]
			for status, amount := range amountByStatus {
				state := string(status)
				if status == liquid.CommitmentStatusConfirmed {
					state = "active" // backwards compatibility with old db.ProjectCommitmentState enum (TODO: use liquid.CommitmentStatus values in v2 metrics)
				}
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
	for _, serviceType := range slices.Sorted(maps.Keys(serviceInfos)) {
		for dbResourceName, resourceInfo := range serviceInfos[serviceType].Resources {
			behavior := behaviorCache.Get(serviceType, dbResourceName)
			apiIdentity := behavior.IdentityInV1API
			labels := fmt.Sprintf(`resource=%q,service=%q,service_name=%q`,
				apiIdentity.Name, apiIdentity.ServiceType, serviceType,
			)

			_, multiplier := resourceInfo.Unit.Base()
			metric := dataMetric{Labels: labels, Value: float64(multiplier)}
			result["limes_unit_multiplier"] = append(result["limes_unit_multiplier"], metric)

			autogrowCfg, ok := d.Cluster.QuotaDistributionConfigForResource(serviceType, dbResourceName).Autogrow.Unpack()
			if ok {
				metric := dataMetric{Labels: labels, Value: autogrowCfg.GrowthMultiplier}
				result["limes_autogrow_growth_multiplier"] = append(result["limes_autogrow_growth_multiplier"], metric)

				metric = dataMetric{Labels: labels, Value: autogrowCfg.AllowQuotaOvercommitUntilAllocatedPercent}
				result["limes_autogrow_quota_overcommit_threshold_percent"] = append(result["limes_autogrow_quota_overcommit_threshold_percent"], metric)
			}

			for _, duration := range behaviorCache.GetCommitmentBehavior(serviceType, dbResourceName).Durations {
				labels := fmt.Sprintf(`duration=%q,resource=%q,service=%q,service_name=%q`,
					duration.String(), apiIdentity.Name, apiIdentity.ServiceType, serviceType,
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

// Get returns the cached ResourceBehavior for the given service type and resource name.
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

// GetForRate returns the cached RateBehavior for the given service type and rate name.
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

// GetCommitmentBehavior returns the cached ScopedCommitmentBehavior for the given service type and resource name.
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
