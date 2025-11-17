// SPDX-FileCopyrightText: 2019 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package audit

import (
	"fmt"
	"net/http"
	"net/url"

	. "github.com/majewsky/gg/option"
	"github.com/sapcc/go-api-declarations/cadf"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/must"

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

// CommitmentEventTarget contains the structure for rendering a cadf.Event.Target for
// changes regarding commitments.
type CommitmentEventTarget struct {
	DomainID        string
	DomainName      string
	ProjectID       liquid.ProjectUUID
	ProjectName     string
	Commitments     []limesresources.Commitment // must have at least one entry
	WorkflowContext Option[db.CommitmentWorkflowContext]
}

// Render implements the audittools.Target interface.
func (t CommitmentEventTarget) Render() cadf.Resource {
	if len(t.Commitments) == 0 {
		panic("commitmentEventTarget must contain at least one commitment")
	}
	res := cadf.Resource{
		TypeURI:     "service/resources/commitment",
		ID:          t.Commitments[0].UUID,
		DomainID:    t.DomainID,
		DomainName:  t.DomainName,
		ProjectID:   string(t.ProjectID),
		ProjectName: t.ProjectName,
		Attachments: []cadf.Attachment{},
	}
	for idx, commitment := range t.Commitments {
		name := "payload"
		if idx > 0 {
			name = "additional-payload"
		}
		attachment := must.Return(cadf.NewJSONAttachment(name, commitment))
		res.Attachments = append(res.Attachments, attachment)
	}
	workflowContext, ok := t.WorkflowContext.Unpack()
	if ok {
		attachment := must.Return(cadf.NewJSONAttachment("context-payload", workflowContext))
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
