// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"encoding/json"
	"time"

	"github.com/go-gorp/gorp/v3"
	. "github.com/majewsky/gg/option"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
)

// ClusterService contains a record from the `cluster_services` table.
type ClusterService struct {
	ID                 ClusterServiceID  `db:"id"`
	Type               ServiceType       `db:"type"`
	ScrapedAt          Option[time.Time] `db:"scraped_at"` // None if never scraped so far
	ScrapeDurationSecs float64           `db:"scrape_duration_secs"`
	SerializedMetrics  string            `db:"serialized_metrics"`
	NextScrapeAt       time.Time         `db:"next_scrape_at"`
	ScrapeErrorMessage string            `db:"scrape_error_message"`
	// following fields get filled from liquid.ServiceInfo
	LiquidVersion                   int64  `db:"liquid_version"`
	CapacityMetricFamiliesJSON      string `db:"capacity_metric_families_json"`
	UsageMetricFamiliesJSON         string `db:"usage_metric_families_json"`
	UsageReportNeedsProjectMetadata bool   `db:"usage_report_needs_project_metadata"`
	QuotaUpdateNeedsProjectMetadata bool   `db:"quota_update_needs_project_metadata"`
}

// ClusterResource contains a record from the `cluster_resources` table.
type ClusterResource struct {
	ID        ClusterResourceID   `db:"id"`
	ServiceID ClusterServiceID    `db:"service_id"`
	Name      liquid.ResourceName `db:"name"`
	// following fields get filled from liquid.ServiceInfo
	LiquidVersion       int64           `db:"liquid_version"`
	Unit                liquid.Unit     `db:"unit"`
	Topology            liquid.Topology `db:"topology"`
	HasCapacity         bool            `db:"has_capacity"`
	NeedsResourceDemand bool            `db:"needs_resource_demand"`
	HasQuota            bool            `db:"has_quota"`
	AttributesJSON      string          `db:"attributes_json"`
}

// ClusterAZResource contains a record from the `cluster_az_resources` table.
type ClusterAZResource struct {
	ID                ClusterAZResourceID    `db:"id"`
	ResourceID        ClusterResourceID      `db:"resource_id"`
	AvailabilityZone  limes.AvailabilityZone `db:"az"`
	RawCapacity       uint64                 `db:"raw_capacity"`
	Usage             Option[uint64]         `db:"usage"`
	SubcapacitiesJSON string                 `db:"subcapacities"`

	// LastNonzeroRawCapacity is None initially, and gets filled whenever capacity scrape sees a non-zero capacity value.
	// We use this as a signal for ACPQ to distinguish new AZs in buildup that should be ignored for the purposes of base quota overcommit,
	// from existing AZs with faulty capacity recording that should block base quota overcommit.
	LastNonzeroRawCapacity Option[uint64] `db:"last_nonzero_raw_capacity"`
}

// ClusterRate contains a record from the `cluster_rates` table.
type ClusterRate struct {
	ID        ClusterRateID    `db:"id"`
	ServiceID ClusterServiceID `db:"service_id"`
	Name      liquid.RateName  `db:"name"`
	// following fields get filled from liquid.ServiceInfo
	LiquidVersion int64           `db:"liquid_version"`
	Unit          liquid.Unit     `db:"unit"`
	Topology      liquid.Topology `db:"topology"`
	HasUsage      bool            `db:"has_usage"`
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

// ProjectServiceV2 contains a record from the `project_services_v2` table.
type ProjectServiceV2 struct {
	ID                    ProjectServiceID  `db:"id"`
	ProjectID             ProjectID         `db:"project_id"`
	ServiceID             ClusterServiceID  `db:"service_id"`
	ScrapedAt             Option[time.Time] `db:"scraped_at"` // None if never scraped so far
	CheckedAt             Option[time.Time] `db:"checked_at"`
	NextScrapeAt          time.Time         `db:"next_scrape_at"`
	Stale                 bool              `db:"stale"`
	ScrapeDurationSecs    float64           `db:"scrape_duration_secs"`
	ScrapeErrorMessage    string            `db:"scrape_error_message"`
	SerializedScrapeState string            `db:"serialized_scrape_state"`
	SerializedMetrics     string            `db:"serialized_metrics"`
	QuotaDesyncedAt       Option[time.Time] `db:"quota_desynced_at"` // None if all quota = backend quota
	QuotaSyncDurationSecs float64           `db:"quota_sync_duration_secs"`
}

// ProjectResourceV2 contains a record from the `project_resources_v2` table. Quota
// values are NULL for resources that do not track quota.
type ProjectResourceV2 struct {
	ID                       ProjectResourceID `db:"id"`
	ProjectID                ProjectID         `db:"project_id"`
	ResourceID               ClusterResourceID `db:"resource_id"`
	Quota                    Option[uint64]    `db:"quota"`
	BackendQuota             Option[int64]     `db:"backend_quota"`
	Forbidden                bool              `db:"forbidden"`
	MaxQuotaFromOutsideAdmin Option[uint64]    `db:"max_quota_from_outside_admin"`
	MaxQuotaFromLocalAdmin   Option[uint64]    `db:"max_quota_from_local_admin"`
	OverrideQuotaFromConfig  Option[uint64]    `db:"override_quota_from_config"`
}

// ProjectAZResourceV2 contains a record from the `project_az_resources_v2` table.
type ProjectAZResourceV2 struct {
	ID                  ProjectAZResourceID `db:"id"`
	ProjectID           ProjectID           `db:"project_id"`
	AZResourceID        ClusterAZResourceID `db:"az_resource_id"`
	Quota               Option[uint64]      `db:"quota"`
	BackendQuota        Option[int64]       `db:"backend_quota"`
	Usage               uint64              `db:"usage"`
	PhysicalUsage       Option[uint64]      `db:"physical_usage"`
	SubresourcesJSON    string              `db:"subresources"`
	HistoricalUsageJSON string              `db:"historical_usage"`
}

// ProjectRateV2 contains a record from the `project_rates_v2` table.
type ProjectRateV2 struct {
	ID            ProjectRateID             `db:"id"`
	ProjectID     ProjectID                 `db:"project_id"`
	RateID        ClusterRateID             `db:"rate_id"`
	Limit         Option[uint64]            `db:"rate_limit"`      // None for rates that don't have a limit (just a usage)
	Window        Option[limesrates.Window] `db:"window_ns"`       // None for rates that don't have a limit (just a usage)
	UsageAsBigint string                    `db:"usage_as_bigint"` // empty for rates that don't have a usage (just a limit)
	// ^ NOTE: Postgres has a NUMERIC type that would be large enough to hold an
	//  uint128, but Go does not have a uint128 builtin, so it's easier to just
	//  use strings throughout and cast into bigints in the scraper only.
}

// ProjectCommitmentV2 contains a record from the `project_commitments_v2` table.
type ProjectCommitmentV2 struct {
	ID           ProjectCommitmentID               `db:"id"`
	UUID         ProjectCommitmentUUID             `db:"uuid"`
	ProjectID    ProjectID                         `db:"project_id"`
	AZResourceID ClusterAZResourceID               `db:"az_resource_id"`
	Amount       uint64                            `db:"amount"`
	Duration     limesresources.CommitmentDuration `db:"duration"`
	CreatedAt    time.Time                         `db:"created_at"`
	CreatorUUID  string                            `db:"creator_uuid"` // format: "username@userdomainname"
	CreatorName  string                            `db:"creator_name"`
	ConfirmBy    Option[time.Time]                 `db:"confirm_by"`
	ConfirmedAt  Option[time.Time]                 `db:"confirmed_at"`
	ExpiresAt    time.Time                         `db:"expires_at"`

	// Commitments can be superseded due to splits, conversions or merges.
	// The context columns contain information about the reason and related commitments
	SupersededAt         Option[time.Time]       `db:"superseded_at"`
	CreationContextJSON  json.RawMessage         `db:"creation_context_json"`
	SupersedeContextJSON Option[json.RawMessage] `db:"supersede_context_json"`
	RenewContextJSON     Option[json.RawMessage] `db:"renew_context_json"`

	// For a commitment to be transferred between projects, it must first be
	// marked for transfer in the source project. Then a new commitment can be
	// created in the target project to supersede the transferable commitment.
	//
	// While a commitment is marked for transfer, it does not count towards quota
	// calculation, but it still blocks capacity and still counts towards billing.
	TransferStatus limesresources.CommitmentTransferStatus `db:"transfer_status"`
	TransferToken  Option[string]                          `db:"transfer_token"`

	// This column is technically redundant, since the state can be derived from
	// the values of other fields. But having this field simplifies lots of
	// queries significantly because we do not need to carry a NOW() argument into
	// the query, and complex conditions like `WHERE superseded_at IS NULL AND
	// expires_at > $now AND confirmed_at IS NULL AND confirm_by < $now` become
	// simple readable conditions like `WHERE state = 'pending'`.
	//
	// This field is updated by the CapacityScrapeJob.
	State CommitmentState `db:"state"`

	// During commitment planning, a user can specify
	// if a mail should be sent after the commitments confirmation.
	NotifyOnConfirm bool `db:"notify_on_confirm"`

	// If commitments are about to expire, they get added into the mail queue.
	// This attribute helps to identify commitments that are already queued.
	NotifiedForExpiration bool `db:"notified_for_expiration"`
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

// CommitmentWorkflowContext is the type definition for the JSON payload in the
// CreationContextJSON and SupersedeContextJSON fields of type ProjectCommitment.
type CommitmentWorkflowContext struct {
	Reason                 CommitmentReason        `json:"reason"`
	RelatedCommitmentIDs   []ProjectCommitmentID   `json:"related_ids,omitempty"` // TODO: remove when v1 API is removed (v2 API uses only UUIDs to refer to commitments)
	RelatedCommitmentUUIDs []ProjectCommitmentUUID `json:"related_uuids,omitempty"`
}

// CommitmentReason is an enum. It appears in type CommitmentWorkflowContext.
type CommitmentReason string

const (
	CommitmentReasonCreate  CommitmentReason = "create"
	CommitmentReasonSplit   CommitmentReason = "split"
	CommitmentReasonConvert CommitmentReason = "convert"
	CommitmentReasonMerge   CommitmentReason = "merge"
	CommitmentReasonRenew   CommitmentReason = "renew"
)

type MailNotification struct {
	ID                int64     `db:"id"`
	ProjectID         ProjectID `db:"project_id"`
	Subject           string    `db:"subject"`
	Body              string    `db:"body"`
	NextSubmissionAt  time.Time `db:"next_submission_at"`
	FailedSubmissions int64     `db:"failed_submissions"`
}

// initGorp is used by Init() to setup the ORM part of the database connection.
func initGorp(db *gorp.DbMap) {
	db.AddTableWithName(ClusterService{}, "cluster_services").SetKeys(true, "id")
	db.AddTableWithName(ClusterResource{}, "cluster_resources").SetKeys(true, "id")
	db.AddTableWithName(ClusterRate{}, "cluster_rates").SetKeys(true, "id")
	db.AddTableWithName(ClusterAZResource{}, "cluster_az_resources").SetKeys(true, "id")
	db.AddTableWithName(Domain{}, "domains").SetKeys(true, "id")
	db.AddTableWithName(Project{}, "projects").SetKeys(true, "id")
	db.AddTableWithName(ProjectServiceV2{}, "project_services_v2").SetKeys(true, "id")
	db.AddTableWithName(ProjectResourceV2{}, "project_resources_v2").SetKeys(true, "id")
	db.AddTableWithName(ProjectAZResourceV2{}, "project_az_resources_v2").SetKeys(true, "id")
	db.AddTableWithName(ProjectRateV2{}, "project_rates_v2").SetKeys(true, "id")
	db.AddTableWithName(ProjectCommitmentV2{}, "project_commitments_v2").SetKeys(true, "id")
	db.AddTableWithName(MailNotification{}, "project_mail_notifications").SetKeys(true, "id")
}
