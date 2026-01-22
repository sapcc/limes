// SPDX-FileCopyrightText: 2019 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package audit

import (
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"

	. "github.com/majewsky/gg/option"
	"github.com/sapcc/go-api-declarations/cadf"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/must"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

// MaxQuotaEventTarget renders a cadf.Event.Target for a max_quota change event.
type MaxQuotaEventTarget struct {
	DomainID        string
	DomainName      string
	ProjectID       liquid.ProjectUUID
	ProjectName     string
	ServiceType     limes.ServiceType
	ResourceName    limesresources.ResourceName
	RequestedChange MaxQuotaChange
}

// MaxQuotaChange appears in type MaxQuotaEventTarget.
type MaxQuotaChange struct {
	OldValue Option[uint64] `json:"oldMaxQuota"`
	NewValue Option[uint64] `json:"newMaxQuota"`
}

// Render implements the audittools.Target interface.
func (t MaxQuotaEventTarget) Render() cadf.Resource {
	return cadf.Resource{
		TypeURI:     fmt.Sprintf("service/%s/%s/max-quota", t.ServiceType, t.ResourceName),
		ID:          string(t.ProjectID),
		DomainID:    t.DomainID,
		DomainName:  t.DomainName,
		ProjectID:   string(t.ProjectID),
		ProjectName: t.ProjectName,
		Attachments: []cadf.Attachment{
			must.Return(cadf.NewJSONAttachment("payload", t.RequestedChange)),
		},
	}
}

// AutogrowthEventTarget contains the structure for rendering a cadf.Event.Target for
// changes regarding the forbid-autogrowth flag.
type AutogrowthEventTarget struct {
	DomainID         string
	DomainName       string
	ProjectID        liquid.ProjectUUID
	ProjectName      string
	ServiceType      limes.ServiceType
	ResourceName     limesresources.ResourceName
	AutogrowthChange AutogrowthChange
}

// AutogrowthChange appears in type AutogrowthEventTarget.
type AutogrowthChange struct {
	ForbidAutogrowth bool `json:"forbid_autogrowth"`
}

// Render implements the audittools.Target interface.
func (t AutogrowthEventTarget) Render() cadf.Resource {
	return cadf.Resource{
		TypeURI:     fmt.Sprintf("service/%s/%s/forbid-autogrowth", t.ServiceType, t.ResourceName),
		ID:          string(t.ProjectID),
		DomainID:    t.DomainID,
		DomainName:  t.DomainName,
		ProjectID:   string(t.ProjectID),
		ProjectName: t.ProjectName,
		Attachments: []cadf.Attachment{
			must.Return(cadf.NewJSONAttachment("payload", t.AutogrowthChange)),
		},
	}
}

// RateLimitEventTarget contains the structure for rendering a cadf.Event.Target for
// changes regarding rate limits
type RateLimitEventTarget struct {
	DomainID    string
	DomainName  string
	ProjectID   liquid.ProjectUUID
	ProjectName string
	ServiceType limes.ServiceType
	Name        limesrates.RateName
	Payload     RateLimitChange
}

// RateLimitChange appears in type rateLimitEventTarget.
type RateLimitChange struct {
	Unit         limes.Unit        `json:"unit,omitempty"`
	OldLimit     uint64            `json:"oldLimit"`
	NewLimit     uint64            `json:"newLimit"`
	OldWindow    limesrates.Window `json:"oldWindow"`
	NewWindow    limesrates.Window `json:"newWindow"`
	RejectReason string            `json:"rejectReason,omitempty"`
}

// Render implements the audittools.Target interface.
func (t RateLimitEventTarget) Render() cadf.Resource {
	return cadf.Resource{
		TypeURI:     fmt.Sprintf("service/%s/%s/rates", t.ServiceType, t.Name),
		ID:          string(t.ProjectID),
		DomainID:    t.DomainID,
		DomainName:  t.DomainName,
		ProjectID:   string(t.ProjectID),
		ProjectName: t.ProjectName,
		Attachments: []cadf.Attachment{
			must.Return(cadf.NewJSONAttachment("payload", t.Payload)),
		},
	}
}

// EnsureLiquidProjectMetadata guarantees that the given liquid.CommitmentChangeRequest
// contains project metadata for the given db.Project and db.Domain.
// The functions should be used before passing a liquid.CommitmentChangeRequest into an
// audit.CommitmentAttributeChangeset to be logged for auditing. For auditing purposes,
// the project metadata must be filled. It is important to call it for all involved projects.
func EnsureLiquidProjectMetadata(ccr liquid.CommitmentChangeRequest, project db.Project, domain db.Domain, serviceInfo liquid.ServiceInfo) liquid.CommitmentChangeRequest {
	pcc := ccr.ByProject[project.UUID]
	pcc.ProjectMetadata = Some(core.KeystoneProjectFromDB(project, core.KeystoneDomainFromDB(domain)).ForLiquid())
	ccr.ByProject[project.UUID] = pcc
	return ccr
}

// redactLiquidProjectMetadataNames removes ProjectMedata of a
// liquid.CommitmentChangeRequest. It is used to enable information-leak-free logging
// of commitment changes where multiple projects are involved.
func redactLiquidProjectMetadataNames(ccr liquid.CommitmentChangeRequest) liquid.CommitmentChangeRequest {
	for projectUUID, pcc := range ccr.ByProject {
		pcc.ProjectMetadata = None[liquid.ProjectMetadata]()
		ccr.ByProject[projectUUID] = pcc
	}
	return ccr
}

// CommitmentAttributeChangeset contains changes, which are not included in
// liquid.CommitmentChangeRequest, but are relevant for auditing.
type CommitmentAttributeChangeset struct {
	OldTransferStatus Option[limesresources.CommitmentTransferStatus] // can be None, when the TransferStatus is stable
	NewTransferStatus Option[limesresources.CommitmentTransferStatus] // can be None, when the TransferStatus is stable
}

// CommitmentEventTarget contains the structure for rendering a cadf.Event.Target for
// changes regarding commitments. It does not implement audittools.Target interface,
// because we need to replicate all changes to all affected projects first and
// additionally need to redact project and domain names.
type CommitmentEventTarget struct {
	// must have at least one project, with one resource, with one commitment
	CommitmentChangeRequest liquid.CommitmentChangeRequest
	// can have one entry per commitment UUID
	CommitmentAttributeChangeset map[liquid.CommitmentUUID]CommitmentAttributeChangeset
}

// ReplicateForAllProjects takes an audittools.Event and generates
// one audittools.Event per project affected in the CommitmentEventTarget, placing
// the richCommitmentEventTarget for that project into the Target field.
// It also redacts project and domain names from the CommitmentChangeRequest
// to avoid information leaks in audit logs.
func (t CommitmentEventTarget) ReplicateForAllProjects(event audittools.Event) []audittools.Event {
	// sort, to make audit event order deterministic
	projects := slices.Sorted(maps.Keys(t.CommitmentChangeRequest.ByProject))
	var result []audittools.Event
	projectMetadataByProjectUUID := make(map[liquid.ProjectUUID]Option[liquid.ProjectMetadata])
	// copy project metadata to save from redaction
	for projectUUID, pcc := range t.CommitmentChangeRequest.ByProject {
		projectMetadataByProjectUUID[projectUUID] = pcc.ProjectMetadata
	}

	for _, projectID := range projects {
		projectMetadata := projectMetadataByProjectUUID[projectID]
		if pm, exists := projectMetadata.Unpack(); !exists {
			panic("attempted to create audit event target from CommitmentChangeRequest without ProjectMetadata")
		} else {
			result = append(result, audittools.Event{
				Time:       event.Time,
				Request:    event.Request,
				User:       event.User,
				ReasonCode: event.ReasonCode,
				Action:     event.Action,
				Target: richCommitmentEventTarget{
					DomainID:                     pm.Domain.UUID,
					DomainName:                   pm.Domain.Name,
					ProjectID:                    liquid.ProjectUUID(pm.UUID),
					ProjectName:                  pm.Name,
					CommitmentChangeRequest:      redactLiquidProjectMetadataNames(t.CommitmentChangeRequest),
					CommitmentAttributeChangeset: t.CommitmentAttributeChangeset,
				},
			})
		}
	}
	return result
}

// richCommitmentEventTarget is a CommitmentEventTarget that is specific to one project.
// It implements audittools.Target interface and has redacted project and domain names within
// the CommitmentChangeRequest. It is not exported, so that only CommitmentEventTarget can create it.
type richCommitmentEventTarget struct {
	DomainID                     string
	DomainName                   string
	ProjectID                    liquid.ProjectUUID
	ProjectName                  string
	CommitmentChangeRequest      liquid.CommitmentChangeRequest
	CommitmentAttributeChangeset map[liquid.CommitmentUUID]CommitmentAttributeChangeset
}

// Render implements the audittools.Target interface.
func (t richCommitmentEventTarget) Render() cadf.Resource {
	var firstCommitment liquid.Commitment
outer:
	for _, pcc := range t.CommitmentChangeRequest.ByProject {
		for _, rcc := range pcc.ByResource {
			for _, commitment := range rcc.Commitments {
				firstCommitment = commitment
				break outer
			}
		}
	}
	if firstCommitment.UUID == "" {
		panic("commitmentEventTarget must contain at least one commitment")
	}

	res := cadf.Resource{
		TypeURI:     "service/resources/commitment",
		ID:          string(firstCommitment.UUID),
		DomainID:    t.DomainID,
		DomainName:  t.DomainName,
		ProjectID:   string(t.ProjectID),
		ProjectName: t.ProjectName,
	}

	attachment := must.Return(cadf.NewJSONAttachment("payload", t.CommitmentChangeRequest))
	res.Attachments = append(res.Attachments, attachment)
	if len(t.CommitmentAttributeChangeset) > 0 {
		attachment = must.Return(cadf.NewJSONAttachment("context-payload", t.CommitmentAttributeChangeset))
		res.Attachments = append(res.Attachments, attachment)
	}

	return res
}

// CollectorUserInfo is an audittools.UserInfo representing a
// collector task (which does not have a corresponding OpenStack user).
// It is used to fill the audit events generated by the collector.
type CollectorUserInfo struct {
	TaskName string
}

// AsInitiator implements the audittools.UserInfo interface.
func (u CollectorUserInfo) AsInitiator(_ cadf.Host) cadf.Resource {
	res := cadf.Resource{
		TypeURI: "service/resources/collector-task",
		Name:    u.TaskName,
		Domain:  "limes",
		ID:      u.TaskName,
	}
	return res
}

// CollectorDummyRequest can be put in the Request field of an audittools.Event.
var CollectorDummyRequest = &http.Request{URL: &url.URL{
	Scheme: "http",
	Host:   "localhost",
	Path:   "limes-collect",
}}

// Context collects the above arguments that business logic methods
// need only for generating audit events.
type Context struct {
	UserIdentity audittools.UserInfo
	Request      *http.Request
}
