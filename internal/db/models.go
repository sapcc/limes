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
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
)

// ClusterCapacitor contains a record from the `cluster_capacitors` table.
type ClusterCapacitor struct {
	CapacitorID        string     `db:"capacitor_id"`
	ScrapedAt          *time.Time `db:"scraped_at"` // pointer type to allow for NULL value
	ScrapeDurationSecs float64    `db:"scrape_duration_secs"`
	SerializedMetrics  string     `db:"serialized_metrics"`
	NextScrapeAt       time.Time  `db:"next_scrape_at"`
	ScrapeErrorMessage string     `db:"scrape_error_message"`
}

// ClusterService contains a record from the `cluster_services` table.
type ClusterService struct {
	ID   ClusterServiceID  `db:"id"`
	Type limes.ServiceType `db:"type"`
}

// ClusterResource contains a record from the `cluster_resources` table.
type ClusterResource struct {
	ID          ClusterResourceID           `db:"id"`
	CapacitorID string                      `db:"capacitor_id"`
	ServiceID   ClusterServiceID            `db:"service_id"`
	Name        limesresources.ResourceName `db:"name"`
}

// Ref returns the ResourceRef for this resource.
func (r ClusterResource) Ref() ResourceRef[ClusterServiceID] {
	return ResourceRef[ClusterServiceID]{r.ServiceID, r.Name}
}

// ClusterAZResource contains a record from the `cluster_az_resources` table.
type ClusterAZResource struct {
	ID                ClusterAZResourceID    `db:"id"`
	ResourceID        ClusterResourceID      `db:"resource_id"`
	AvailabilityZone  limes.AvailabilityZone `db:"az"`
	RawCapacity       uint64                 `db:"raw_capacity"`
	Usage             *uint64                `db:"usage"`
	SubcapacitiesJSON string                 `db:"subcapacities"`
}

// Domain contains a record from the `domains` table.
type Domain struct {
	ID   DomainID `db:"id"`
	Name string   `db:"name"`
	UUID string   `db:"uuid"`
}

// Project contains a record from the `projects` table.
type Project struct {
	ID         ProjectID `db:"id"`
	DomainID   DomainID  `db:"domain_id"`
	Name       string    `db:"name"`
	UUID       string    `db:"uuid"`
	ParentUUID string    `db:"parent_uuid"`
}

// ProjectService contains a record from the `project_services` table.
type ProjectService struct {
	ID                      ProjectServiceID  `db:"id"`
	ProjectID               ProjectID         `db:"project_id"`
	Type                    limes.ServiceType `db:"type"`
	ScrapedAt               *time.Time        `db:"scraped_at"` // pointer type to allow for NULL value
	CheckedAt               *time.Time        `db:"checked_at"`
	NextScrapeAt            time.Time         `db:"next_scrape_at"`
	Stale                   bool              `db:"stale"`
	ScrapeDurationSecs      float64           `db:"scrape_duration_secs"`
	ScrapeErrorMessage      string            `db:"scrape_error_message"`
	RatesScrapedAt          *time.Time        `db:"rates_scraped_at"` // same as above
	RatesCheckedAt          *time.Time        `db:"rates_checked_at"`
	RatesNextScrapeAt       time.Time         `db:"rates_next_scrape_at"`
	RatesStale              bool              `db:"rates_stale"`
	RatesScrapeDurationSecs float64           `db:"rates_scrape_duration_secs"`
	RatesScrapeState        string            `db:"rates_scrape_state"`
	RatesScrapeErrorMessage string            `db:"rates_scrape_error_message"`
	SerializedMetrics       string            `db:"serialized_metrics"`
	QuotaDesyncedAt         *time.Time        `db:"quota_desynced_at"`
	QuotaSyncDurationSecs   float64           `db:"quota_sync_duration_secs"`
}

// Ref converts a ProjectService into its ProjectServiceRef.
func (s ProjectService) Ref() ServiceRef[ProjectServiceID] {
	return ServiceRef[ProjectServiceID]{ID: s.ID, Type: s.Type}
}

// ProjectResource contains a record from the `project_resources` table. Quota
// values are NULL for resources that do not track quota.
type ProjectResource struct {
	ID                      ProjectResourceID           `db:"id"`
	ServiceID               ProjectServiceID            `db:"service_id"`
	Name                    limesresources.ResourceName `db:"name"`
	Quota                   *uint64                     `db:"quota"`
	BackendQuota            *int64                      `db:"backend_quota"`
	MinQuotaFromBackend     *uint64                     `db:"min_quota_from_backend"`
	MaxQuotaFromBackend     *uint64                     `db:"max_quota_from_backend"`
	MaxQuotaFromAdmin       *uint64                     `db:"max_quota_from_admin"`
	OverrideQuotaFromConfig *uint64                     `db:"override_quota_from_config"`
}

// Ref returns the ResourceRef for this resource.
func (r ProjectResource) Ref() ResourceRef[ProjectServiceID] {
	return ResourceRef[ProjectServiceID]{r.ServiceID, r.Name}
}

// ProjectAZResource contains a record from the `project_az_resources` table.
type ProjectAZResource struct {
	ID                  ProjectAZResourceID    `db:"id"`
	ResourceID          ProjectResourceID      `db:"resource_id"`
	AvailabilityZone    limes.AvailabilityZone `db:"az"`
	Quota               *uint64                `db:"quota"`
	Usage               uint64                 `db:"usage"`
	PhysicalUsage       *uint64                `db:"physical_usage"`
	SubresourcesJSON    string                 `db:"subresources"`
	HistoricalUsageJSON string                 `db:"historical_usage"`
}

// ProjectRate contains a record from the `project_rates` table.
type ProjectRate struct {
	ServiceID     ProjectServiceID    `db:"service_id"`
	Name          limesrates.RateName `db:"name"`
	Limit         *uint64             `db:"rate_limit"`      // nil for rates that don't have a limit (just a usage)
	Window        *limesrates.Window  `db:"window_ns"`       // nil for rates that don't have a limit (just a usage)
	UsageAsBigint string              `db:"usage_as_bigint"` // empty for rates that don't have a usage (just a limit)
	// ^ NOTE: Postgres has a NUMERIC type that would be large enough to hold an
	//  uint128, but Go does not have a uint128 builtin, so it's easier to just
	//  use strings throughout and cast into bigints in the scraper only.
}

// ProjectCommitment contains a record from the `project_commitments` table.
type ProjectCommitment struct {
	ID           ProjectCommitmentID               `db:"id"`
	AZResourceID ProjectAZResourceID               `db:"az_resource_id"`
	Amount       uint64                            `db:"amount"`
	Duration     limesresources.CommitmentDuration `db:"duration"`
	CreatedAt    time.Time                         `db:"created_at"`
	CreatorUUID  string                            `db:"creator_uuid"` // format: "username@userdomainname"
	CreatorName  string                            `db:"creator_name"`
	ConfirmBy    *time.Time                        `db:"confirm_by"`
	ConfirmedAt  *time.Time                        `db:"confirmed_at"`
	ExpiresAt    time.Time                         `db:"expires_at"`

	// A commitment can be superseded e.g. by splitting it into smaller parts.
	// When that happens, the new commitments will point to the one that they
	// superseded through the PredecessorID field.
	SupersededAt  *time.Time           `db:"superseded_at"`
	PredecessorID *ProjectCommitmentID `db:"predecessor_id"`

	// For a commitment to be transferred between projects, it must first be
	// marked for transfer in the source project. Then a new commitment can be
	// created in the target project to supersede the transferable commitment.
	//
	// While a commitment is marked for transfer, it does not count towards quota
	// calculation, but it still blocks capacity and still counts towards billing.
	TransferStatus limesresources.CommitmentTransferStatus `db:"transfer_status"`
	TransferToken  *string                                 `db:"transfer_token"`

	// This column is technically redundant, since the state can be derived from
	// the values of other fields. But having this field simplifies lots of
	// queries significantly because we do not need to carry a NOW() argument into
	// the query, and complex conditions like `WHERE superseded_at IS NULL AND
	// expires_at > $now AND confirmed_at IS NULL AND confirm_by < $now` become
	// simple readable conditions like `WHERE state = 'pending'`.
	//
	// This field is updated by the CapacityScrapeJob.
	State CommitmentState `db:"state"`
}

// CommitmentState is an enum. The possible values below are sorted in roughly chronological order.
type CommitmentState string

const (
	CommitmentStatePlanned    CommitmentState = "planned"
	CommitmentStatePending    CommitmentState = "pending"
	CommitmentStateActive     CommitmentState = "active"
	CommitmentStateSuperseded CommitmentState = "superseded"
	CommitmentStateExpired    CommitmentState = "expired"
)

// initGorp is used by Init() to setup the ORM part of the database connection.
func initGorp(db *gorp.DbMap) {
	db.AddTableWithName(ClusterCapacitor{}, "cluster_capacitors").SetKeys(false, "capacitor_id")
	db.AddTableWithName(ClusterService{}, "cluster_services").SetKeys(true, "id")
	db.AddTableWithName(ClusterResource{}, "cluster_resources").SetKeys(true, "id")
	db.AddTableWithName(ClusterAZResource{}, "cluster_az_resources").SetKeys(true, "id")
	db.AddTableWithName(Domain{}, "domains").SetKeys(true, "id")
	db.AddTableWithName(Project{}, "projects").SetKeys(true, "id")
	db.AddTableWithName(ProjectService{}, "project_services").SetKeys(true, "id")
	db.AddTableWithName(ProjectResource{}, "project_resources").SetKeys(true, "id")
	db.AddTableWithName(ProjectAZResource{}, "project_az_resources").SetKeys(true, "id")
	db.AddTableWithName(ProjectRate{}, "project_rates").SetKeys(false, "service_id", "name")
	db.AddTableWithName(ProjectCommitment{}, "project_commitments").SetKeys(true, "id")
}
