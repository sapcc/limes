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

package collector

import (
	"database/sql"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/util"
)

////////////////////////////////////////////////////////////////////////////////
// collector metrics

var scrapeSuccessCounter = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "limes_successful_scrapes",
		Help: "Counter for successful scrape operations per Keystone project.",
	},
	[]string{"os_cluster", "service", "service_name"},
)

var scrapeFailedCounter = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "limes_failed_scrapes",
		Help: "Counter for failed scrape operations per Keystone project.",
	},
	[]string{"os_cluster", "service", "service_name"},
)

func init() {
	prometheus.MustRegister(scrapeSuccessCounter)
	prometheus.MustRegister(scrapeFailedCounter)
}

////////////////////////////////////////////////////////////////////////////////
// data metrics

var clusterCapacityGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_cluster_capacity",
		Help: "Reported capacity of a Limes resource for an OpenStack cluster.",
	},
	[]string{"os_cluster", "service", "resource"},
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

var projectUsageGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_project_usage",
		Help: "Actual usage of a Limes resource for an OpenStack project.",
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

//DataMetricsCollector is a prometheus.Collector that submits
//quota/usage/backend quota from an OpenStack cluster as Prometheus metrics.
type DataMetricsCollector struct {
	ClusterID string
}

//Describe implements the prometheus.Collector interface.
func (c *DataMetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	clusterCapacityGauge.Describe(ch)
	domainQuotaGauge.Describe(ch)
	projectQuotaGauge.Describe(ch)
	projectUsageGauge.Describe(ch)
	projectBackendQuotaGauge.Describe(ch)
}

var clusterMetricsQuery = `
	SELECT cs.type, cr.name, cr.capacity
	  FROM cluster_services cs
	  JOIN cluster_resources cr ON cr.service_id = cs.id
	 WHERE cs.cluster_id = $1
`

var domainMetricsQuery = `
	SELECT d.name, d.uuid, ds.type, dr.name, dr.quota
	  FROM domains d
	  JOIN domain_services ds ON ds.domain_id = d.id
	  JOIN domain_resources dr ON dr.service_id = ds.id
	 WHERE d.cluster_id = $1
`

var projectMetricsQuery = `
	SELECT d.name, d.uuid, p.name, p.uuid, ps.type, pr.name, pr.quota, pr.usage, pr.backend_quota
	  FROM domains d
	  JOIN projects p ON p.domain_id = d.id
	  JOIN project_services ps ON ps.project_id = p.id
	  JOIN project_resources pr ON pr.service_id = ps.id
	 WHERE d.cluster_id = $1
`

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
	domainQuotaGauge.Describe(descCh)
	domainQuotaDesc := <-descCh
	projectQuotaGauge.Describe(descCh)
	projectQuotaDesc := <-descCh
	projectUsageGauge.Describe(descCh)
	projectUsageDesc := <-descCh
	projectBackendQuotaGauge.Describe(descCh)
	projectBackendQuotaDesc := <-descCh

	//fetch values
	queryArgs := []interface{}{c.ClusterID}
	err := db.ForeachRow(db.DB, clusterMetricsQuery, queryArgs, func(rows *sql.Rows) error {
		var (
			serviceType  string
			resourceName string
			capacity     uint64
		)
		err := rows.Scan(&serviceType, &resourceName, &capacity)
		if err != nil {
			return err
		}
		ch <- prometheus.MustNewConstMetric(
			clusterCapacityDesc,
			prometheus.GaugeValue, float64(capacity),
			c.ClusterID, serviceType, resourceName,
		)
		return nil
	})
	if err != nil {
		util.LogError("collect cluster metrics failed: " + err.Error())
	}

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
			c.ClusterID, domainName, domainUUID, serviceType, resourceName,
		)
		return nil
	})
	if err != nil {
		util.LogError("collect domain metrics failed: " + err.Error())
	}

	err = db.ForeachRow(db.DB, projectMetricsQuery, queryArgs, func(rows *sql.Rows) error {
		var (
			domainName   string
			domainUUID   string
			projectName  string
			projectUUID  string
			serviceType  string
			resourceName string
			quota        uint64
			usage        uint64
			backendQuota int64
		)
		err := rows.Scan(&domainName, &domainUUID, &projectName, &projectUUID, &serviceType, &resourceName, &quota, &usage, &backendQuota)
		if err != nil {
			return err
		}

		ch <- prometheus.MustNewConstMetric(
			projectQuotaDesc,
			prometheus.GaugeValue, float64(quota),
			c.ClusterID, domainName, domainUUID, projectName, projectUUID, serviceType, resourceName,
		)
		ch <- prometheus.MustNewConstMetric(
			projectUsageDesc,
			prometheus.GaugeValue, float64(usage),
			c.ClusterID, domainName, domainUUID, projectName, projectUUID, serviceType, resourceName,
		)
		ch <- prometheus.MustNewConstMetric(
			projectBackendQuotaDesc,
			prometheus.GaugeValue, float64(backendQuota),
			c.ClusterID, domainName, domainUUID, projectName, projectUUID, serviceType, resourceName,
		)
		return nil
	})
	if err != nil {
		util.LogError("collect project metrics failed: " + err.Error())
	}
}
