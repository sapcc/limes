// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"encoding/json"
	"time"

	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	. "go.xyrillian.de/gg/option"
	"go.xyrillian.de/oblast"
)

// Service contains a record from the `services` table.
type Service struct {
	ID                 ServiceID         `db:"id,auto"`
	Type               ServiceType       `db:"type"`
	DisplayName        string            `db:"display_name"`
	ScrapedAt          Option[time.Time] `db:"scraped_at"` // None if never scraped so far
	ScrapeDurationSecs float64           `db:"scrape_duration_secs"`
	SerializedMetrics  string            `db:"serialized_metrics"`
	NextScrapeAt       time.Time         `db:"next_scrape_at"`
	ScrapeErrorMessage string            `db:"scrape_error_message"`
	// following fields get filled from liquid.ServiceInfo
	LiquidVersion                          int64  `db:"liquid_version"`
	CapacityMetricFamiliesJSON             string `db:"capacity_metric_families_json"`
	UsageMetricFamiliesJSON                string `db:"usage_metric_families_json"`
	UsageReportNeedsProjectMetadata        bool   `db:"usage_report_needs_project_metadata"`
	QuotaUpdateNeedsProjectMetadata        bool   `db:"quota_update_needs_project_metadata"`
	CommitmentHandlingNeedsProjectMetadata bool   `db:"commitment_handling_needs_project_metadata"`
}

// ServiceStore is the [oblast.Store] for the `services` table.
var ServiceStore = oblast.MustNewStore[Service](oblast.PostgresDialect(),
	oblast.TableNameIs("services"),
	oblast.PrimaryKeyIs("id"),
)

// ServiceByTypeIndex is an [oblast.RuntimeIndex] using the Service.Type field.
var ServiceByTypeIndex = oblast.NewRuntimeIndex(func(s Service) ServiceType { return s.Type })

// Resource contains a record from the `resources` table.
type Resource struct {
	ID          ResourceID          `db:"id,auto"`
	ServiceID   ServiceID           `db:"service_id"`
	Name        liquid.ResourceName `db:"name"`
	DisplayName string              `db:"display_name"`
	CategoryID  Option[CategoryID]  `db:"category_id"`
	// a unique identifier for this record in the form "servicetype/resourcename"; mostly intended for manual lookup
	Path ResourcePath `db:"path"`

	// following fields get filled from liquid.ServiceInfo
	LiquidVersion       int64           `db:"liquid_version"`
	Unit                liquid.Unit     `db:"unit"`
	Topology            liquid.Topology `db:"topology"`
	HasCapacity         bool            `db:"has_capacity"`
	NeedsResourceDemand bool            `db:"needs_resource_demand"`
	HasQuota            bool            `db:"has_quota"`
	AttributesJSON      string          `db:"attributes_json"`
	HandlesCommitments  bool            `db:"handles_commitments"`
}

// ResourceStore is the [oblast.Store] for the `resources` table.
var ResourceStore = oblast.MustNewStore[Resource](oblast.PostgresDialect(),
	oblast.TableNameIs("resources"),
	oblast.PrimaryKeyIs("id"),
)

// ResourceByPathIndex is an [oblast.RuntimeIndex] using the Resource.Path field.
var ResourceByPathIndex = oblast.NewRuntimeIndex(func(r Resource) ResourcePath { return r.Path })

// AZResource contains a record from the `az_resources` table.
type AZResource struct {
	ID               AZResourceID           `db:"id,auto"`
	ResourceID       ResourceID             `db:"resource_id"`
	AvailabilityZone limes.AvailabilityZone `db:"az"`
	// a unique identifier for this record in the form "servicetype/resourcename"; mostly intended for manual lookup
	Path AZResourcePath `db:"path"`

	RawCapacity uint64         `db:"raw_capacity"`
	Usage       Option[uint64] `db:"usage"`
	// '' for az=total
	SubcapacitiesJSON string `db:"subcapacities"`

	// LastNonzeroRawCapacity is None initially, and gets filled whenever capacity scrape sees a non-zero capacity value.
	// We use this as a signal for ACPQ to distinguish new AZs in buildup that should be ignored for the purposes of base quota overcommit,
	// from existing AZs with faulty capacity recording that should block base quota overcommit.
	// None for az=total
	LastNonzeroRawCapacity Option[uint64] `db:"last_nonzero_raw_capacity"`
}

// AZResourceStore is the [oblast.Store] for the `az_resources` table.
var AZResourceStore = oblast.MustNewStore[AZResource](oblast.PostgresDialect(),
	oblast.TableNameIs("az_resources"),
	oblast.PrimaryKeyIs("id"),
)

// AZResourceByPathIndex is an [oblast.RuntimeIndex] using the AZResource.Path field.
var AZResourceByPathIndex = oblast.NewRuntimeIndex(func(azr AZResource) AZResourcePath { return azr.Path })

// AZResourceByResourceIDIndex is an [oblast.RuntimeIndex] using the AZResource.ResourceID field.
var AZResourceByResourceIDIndex = oblast.NewRuntimeIndex(func(azr AZResource) ResourceID { return azr.ResourceID })

// Rate contains a record from the `rates` table.
type Rate struct {
	ID          RateID             `db:"id,auto"`
	ServiceID   ServiceID          `db:"service_id"`
	Name        liquid.RateName    `db:"name"`
	DisplayName string             `db:"display_name"`
	CategoryID  Option[CategoryID] `db:"category_id"`
	// a unique identifier for this record in the form "servicetype/ratename"; mostly intended for manual lookup
	Path RatePath `db:"path"`
	// following fields get filled from liquid.ServiceInfo
	LiquidVersion int64           `db:"liquid_version"`
	Unit          liquid.Unit     `db:"unit"`
	Topology      liquid.Topology `db:"topology"`
	HasUsage      bool            `db:"has_usage"`
}

// RateStore is the [oblast.Store] for the `rates` table.
var RateStore = oblast.MustNewStore[Rate](oblast.PostgresDialect(),
	oblast.TableNameIs("rates"),
	oblast.PrimaryKeyIs("id"),
)

// RateByPathIndex is an [oblast.RuntimeIndex] using the Rate.Path field.
var RateByPathIndex = oblast.NewRuntimeIndex(func(r Rate) RatePath { return r.Path })

// Domain contains a record from the `domains` table.
type Domain struct {
	ID   DomainID `db:"id,auto"`
	Name string   `db:"name"`
	UUID string   `db:"uuid"`
}

// DomainStore is the [oblast.Store] for the `domains` table.
var DomainStore = oblast.MustNewStore[Domain](oblast.PostgresDialect(),
	oblast.TableNameIs("domains"),
	oblast.PrimaryKeyIs("id"),
)

// Project contains a record from the `projects` table.
type Project struct {
	ID         ProjectID          `db:"id,auto"`
	DomainID   DomainID           `db:"domain_id"`
	Name       string             `db:"name"`
	UUID       liquid.ProjectUUID `db:"uuid"`
	ParentUUID string             `db:"parent_uuid"`
}

// ProjectStore is the [oblast.Store] for the `projects` table.
var ProjectStore = oblast.MustNewStore[Project](oblast.PostgresDialect(),
	oblast.TableNameIs("projects"),
	oblast.PrimaryKeyIs("id"),
)

// ProjectService contains a record from the `project_services` table.
type ProjectService struct {
	ID                    ProjectServiceID  `db:"id,auto"`
	ProjectID             ProjectID         `db:"project_id"`
	ServiceID             ServiceID         `db:"service_id"`
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

// ProjectServiceStore is the [oblast.Store] for the `project_services` table.
var ProjectServiceStore = oblast.MustNewStore[ProjectService](oblast.PostgresDialect(),
	oblast.TableNameIs("project_services"),
	oblast.PrimaryKeyIs("id"),
)

// ProjectResource contains a record from the `project_resources` table. Quota
// values are NULL for resources that do not track quota.
type ProjectResource struct {
	ID                       ProjectResourceID `db:"id,auto"`
	ProjectID                ProjectID         `db:"project_id"`
	ResourceID               ResourceID        `db:"resource_id"`
	Forbidden                bool              `db:"forbidden"`
	ForbidAutogrowth         bool              `db:"forbid_autogrowth"`
	MaxQuotaFromOutsideAdmin Option[uint64]    `db:"max_quota_from_outside_admin"`
	OverrideQuotaFromConfig  Option[uint64]    `db:"override_quota_from_config"`
}

// ProjectResourceStore is the [oblast.Store] for the `project_resources` table.
var ProjectResourceStore = oblast.MustNewStore[ProjectResource](oblast.PostgresDialect(),
	oblast.TableNameIs("project_resources"),
	oblast.PrimaryKeyIs("id"),
)

// ProjectAZResource contains a record from the `project_az_resources` table.
type ProjectAZResource struct {
	ID           ProjectAZResourceID `db:"id,auto"`
	ProjectID    ProjectID           `db:"project_id"`
	AZResourceID AZResourceID        `db:"az_resource_id"`
	// None if hasQuota=false OR (az=total AND topology=az-separated) OR az=unknown
	Quota Option[uint64] `db:"quota"`
	// None if hasQuota=false OR (az=total AND topology=az-separated) OR (az!=total AND topology!=az-separated) OR az=unknown
	BackendQuota  Option[int64]  `db:"backend_quota"`
	Usage         uint64         `db:"usage"`
	PhysicalUsage Option[uint64] `db:"physical_usage"`
	// '' for az=total
	SubresourcesJSON string `db:"subresources"`
	// '' for az=total
	HistoricalUsageJSON string `db:"historical_usage"`
}

// ProjectAZResourceStore is the [oblast.Store] for the `project_az_resources` table.
var ProjectAZResourceStore = oblast.MustNewStore[ProjectAZResource](oblast.PostgresDialect(),
	oblast.TableNameIs("project_az_resources"),
	oblast.PrimaryKeyIs("id"),
)

// ProjectRate contains a record from the `project_rates` table.
type ProjectRate struct {
	ID            ProjectRateID             `db:"id,auto"`
	ProjectID     ProjectID                 `db:"project_id"`
	RateID        RateID                    `db:"rate_id"`
	Limit         Option[uint64]            `db:"rate_limit"`      // None for rates that don't have a limit (just a usage)
	Window        Option[limesrates.Window] `db:"window_ns"`       // None for rates that don't have a limit (just a usage)
	UsageAsBigint string                    `db:"usage_as_bigint"` // empty for rates that don't have a usage (just a limit)
	// ^ NOTE: Postgres has a NUMERIC type that would be large enough to hold an
	//  uint128, but Go does not have a uint128 builtin, so it's easier to just
	//  use strings throughout and cast into bigints in the scraper only.
}

// ProjectRateStore is the [oblast.Store] for the `project_rates` table.
var ProjectRateStore = oblast.MustNewStore[ProjectRate](oblast.PostgresDialect(),
	oblast.TableNameIs("project_rates"),
	oblast.PrimaryKeyIs("id"),
)

// ProjectCommitment contains a record from the `project_commitments` table.
type ProjectCommitment struct {
	ID           ProjectCommitmentID               `db:"id,auto"`
	UUID         liquid.CommitmentUUID             `db:"uuid"`
	ProjectID    ProjectID                         `db:"project_id"`
	AZResourceID AZResourceID                      `db:"az_resource_id"`
	Amount       uint64                            `db:"amount"`
	Duration     limesresources.CommitmentDuration `db:"duration"`
	CreatedAt    time.Time                         `db:"created_at"`
	CreatorUUID  string                            `db:"creator_uuid"` // format: "username@userdomainname"
	CreatorName  string                            `db:"creator_name"`
	ConfirmBy    Option[time.Time]                 `db:"confirm_by"`
	ConfirmedAt  Option[time.Time]                 `db:"confirmed_at"`
	ExpiresAt    time.Time                         `db:"expires_at"`
	DeletedAt    Option[time.Time]                 `db:"deleted_at"`

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
	// publicly transferred commitments are ordered by the time of their posting
	TransferStartedAt Option[time.Time] `db:"transfer_started_at"`

	// To a certain extent, this column is technically redundant, since the
	// status can often be derived from the values of other fields. For example,
	// a commitment is in status "superseded" iff `SupersededAt.IsSome()`.
	//
	// However, having this field simplifies lots of queries significantly
	// because we do not need to carry a NOW() argument into the query,
	// and complex conditions like `WHERE superseded_at IS NULL AND expires_at > $now AND confirmed_at IS NULL AND confirm_by < $now`
	// become simple readable conditions like `WHERE status IN ('pending', 'guaranteed')`.
	//
	// This field is updated by the CapacityScrapeJob.
	Status liquid.CommitmentStatus `db:"status"`

	// During commitment planning, a user can specify
	// if a mail should be sent after the commitments confirmation.
	NotifyOnConfirm bool `db:"notify_on_confirm"`

	// If commitments are about to expire, they get added into the mail queue.
	// This attribute helps to identify commitments that are already queued.
	NotifiedForExpiration bool `db:"notified_for_expiration"`
}

// ProjectCommitmentStore is the [oblast.Store] for the `project_commitments` table.
var ProjectCommitmentStore = oblast.MustNewStore[ProjectCommitment](oblast.PostgresDialect(),
	oblast.TableNameIs("project_commitments"),
	oblast.PrimaryKeyIs("id"),
)

// CommitmentWorkflowContext is the type definition for the JSON payload in the
// CreationContextJSON and SupersedeContextJSON fields of type ProjectCommitment.
type CommitmentWorkflowContext struct {
	Reason                 CommitmentReason        `json:"reason"`
	RelatedCommitmentIDs   []ProjectCommitmentID   `json:"related_ids,omitempty"` // TODO: remove when v1 API is removed (v2 API uses only UUIDs to refer to commitments)
	RelatedCommitmentUUIDs []liquid.CommitmentUUID `json:"related_uuids,omitempty"`
}

// CommitmentReason is an enum. It appears in type CommitmentWorkflowContext.
type CommitmentReason string

const (
	CommitmentReasonCreate  CommitmentReason = "create"
	CommitmentReasonSplit   CommitmentReason = "split"
	CommitmentReasonConvert CommitmentReason = "convert"
	CommitmentReasonMerge   CommitmentReason = "merge"
	CommitmentReasonRenew   CommitmentReason = "renew"
	CommitmentReasonConsume CommitmentReason = "consume"
)

// MailNotification contains a record from the `project_mail_notifications` table.
type MailNotification struct {
	ID                int64     `db:"id,auto"`
	ProjectID         ProjectID `db:"project_id"`
	Subject           string    `db:"subject"`
	Body              string    `db:"body"`
	NextSubmissionAt  time.Time `db:"next_submission_at"`
	FailedSubmissions int64     `db:"failed_submissions"`
}

// MailNotificationStore is the [oblast.Store] for the `project_mail_notifications` table.
var MailNotificationStore = oblast.MustNewStore[MailNotification](oblast.PostgresDialect(),
	oblast.TableNameIs("project_mail_notifications"),
	oblast.PrimaryKeyIs("id"),
)

// Category contains a record from the `categories` table.
type Category struct {
	ID          CategoryID          `db:"id,auto"`
	Name        liquid.CategoryName `db:"name"`
	DisplayName string              `db:"display_name"`
}

// CategoryStore is the [oblast.Store] for the `categories` table.
var CategoryStore = oblast.MustNewStore[Category](oblast.PostgresDialect(),
	oblast.TableNameIs("categories"),
	oblast.PrimaryKeyIs("id"),
)

// CategoryByIDIndex is an [oblast.RuntimeIndex] using the Category.ID field.
var CategoryByIDIndex = oblast.NewRuntimeIndex(func(c Category) CategoryID { return c.ID })

// CategoryByNameIndex is an [oblast.RuntimeIndex] using the Category.Name field.
var CategoryByNameIndex = oblast.NewRuntimeIndex(func(c Category) liquid.CategoryName { return c.Name })
