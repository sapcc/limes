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

package db

import (
	"time"

	"github.com/go-gorp/gorp/v3"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
)

// ClusterCapacitor contains a record from the `cluster_capacitors` table.
type ClusterCapacitor struct {
	CapacitorID        string     `db:"capacitor_id"`
	ScrapedAt          *time.Time `db:"scraped_at"` //pointer type to allow for NULL value
	ScrapeDurationSecs float64    `db:"scrape_duration_secs"`
	SerializedMetrics  string     `db:"serialized_metrics"`
	NextScrapeAt       time.Time  `db:"next_scrape_at"`
}

// ClusterService contains a record from the `cluster_services` table.
type ClusterService struct {
	ID        int64      `db:"id"`
	Type      string     `db:"type"`
	ScrapedAt *time.Time `db:"scraped_at"` //pointer type to allow for NULL value
}

// ClusterResource contains a record from the `cluster_resources` table.
type ClusterResource struct {
	ServiceID         int64   `db:"service_id"`
	Name              string  `db:"name"`
	RawCapacity       uint64  `db:"capacity"`
	CapacityPerAZJSON string  `db:"capacity_per_az"`
	SubcapacitiesJSON string  `db:"subcapacities"`
	CapacitorID       *string `db:"capacitor_id"` //can be NULL during transition period (TODO: remove after migration 24)
}

// Domain contains a record from the `domains` table.
type Domain struct {
	ID   int64  `db:"id"`
	Name string `db:"name"`
	UUID string `db:"uuid"`
}

// DomainService contains a record from the `domain_services` table.
type DomainService struct {
	ID       int64  `db:"id"`
	DomainID int64  `db:"domain_id"`
	Type     string `db:"type"`
}

// DomainResource contains a record from the `domain_resources` table.
type DomainResource struct {
	ServiceID int64  `db:"service_id"`
	Name      string `db:"name"`
	Quota     uint64 `db:"quota"`
}

// Project contains a record from the `projects` table.
type Project struct {
	ID          int64  `db:"id"`
	DomainID    int64  `db:"domain_id"`
	Name        string `db:"name"`
	UUID        string `db:"uuid"`
	ParentUUID  string `db:"parent_uuid"`
	HasBursting bool   `db:"has_bursting"`
}

// ProjectService contains a record from the `project_services` table.
type ProjectService struct {
	ID                      int64      `db:"id"`
	ProjectID               int64      `db:"project_id"`
	Type                    string     `db:"type"`
	ScrapedAt               *time.Time `db:"scraped_at"` //pointer type to allow for NULL value
	CheckedAt               *time.Time `db:"checked_at"`
	NextScrapeAt            time.Time  `db:"next_scrape_at"`
	Stale                   bool       `db:"stale"`
	ScrapeDurationSecs      float64    `db:"scrape_duration_secs"`
	ScrapeErrorMessage      string     `db:"scrape_error_message"`
	RatesScrapedAt          *time.Time `db:"rates_scraped_at"` //same as above
	RatesCheckedAt          *time.Time `db:"rates_checked_at"`
	RatesNextScrapeAt       time.Time  `db:"rates_next_scrape_at"`
	RatesStale              bool       `db:"rates_stale"`
	RatesScrapeDurationSecs float64    `db:"rates_scrape_duration_secs"`
	RatesScrapeState        string     `db:"rates_scrape_state"`
	RatesScrapeErrorMessage string     `db:"rates_scrape_error_message"`
	SerializedMetrics       string     `db:"serialized_metrics"`
}

// ProjectServiceRef contains only the `ID` and `Type` fields of
// ProjectService. It appears in APIs when not the entire ProjectService entry
// is needed.
type ProjectServiceRef struct {
	ID   int64
	Type string
}

// Ref converts a ProjectService into its ProjectServiceRef.
func (s ProjectService) Ref() ProjectServiceRef {
	return ProjectServiceRef{ID: s.ID, Type: s.Type}
}

// ProjectResource contains a record from the `project_resources` table. Quota
// values are NULL for resources that do not track quota.
type ProjectResource struct {
	ServiceID           int64   `db:"service_id"`
	Name                string  `db:"name"`
	Quota               *uint64 `db:"quota"`
	Usage               uint64  `db:"usage"`
	PhysicalUsage       *uint64 `db:"physical_usage"`
	BackendQuota        *int64  `db:"backend_quota"`
	DesiredBackendQuota *uint64 `db:"desired_backend_quota"`
	SubresourcesJSON    string  `db:"subresources"`
}

// ProjectRate contains a record from the `project_rates` table.
type ProjectRate struct {
	ServiceID     int64              `db:"service_id"`
	Name          string             `db:"name"`
	Limit         *uint64            `db:"rate_limit"`      // nil for rates that don't have a limit (just a usage)
	Window        *limesrates.Window `db:"window_ns"`       // nil for rates that don't have a limit (just a usage)
	UsageAsBigint string             `db:"usage_as_bigint"` // empty for rates that don't have a usage (just a limit)
	//^ NOTE: Postgres has a NUMERIC type that would be large enough to hold an
	//  uint128, but Go does not have a uint128 builtin, so it's easier to just
	//  use strings throughout and cast into bigints in the scraper only.
}

// initGorp is used by Init() to setup the ORM part of the database connection.
func initGorp(db *gorp.DbMap) {
	db.AddTableWithName(ClusterCapacitor{}, "cluster_capacitors").SetKeys(false, "capacitor_id")
	db.AddTableWithName(ClusterService{}, "cluster_services").SetKeys(true, "id")
	db.AddTableWithName(ClusterResource{}, "cluster_resources").SetKeys(false, "service_id", "name")
	db.AddTableWithName(Domain{}, "domains").SetKeys(true, "id")
	db.AddTableWithName(DomainService{}, "domain_services").SetKeys(true, "id")
	db.AddTableWithName(DomainResource{}, "domain_resources").SetKeys(false, "service_id", "name")
	db.AddTableWithName(Project{}, "projects").SetKeys(true, "id")
	db.AddTableWithName(ProjectService{}, "project_services").SetKeys(true, "id")
	db.AddTableWithName(ProjectResource{}, "project_resources").SetKeys(false, "service_id", "name")
	db.AddTableWithName(ProjectRate{}, "project_rates").SetKeys(false, "service_id", "name")
}
