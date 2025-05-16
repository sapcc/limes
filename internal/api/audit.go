// SPDX-FileCopyrightText: 2019 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"fmt"
	"strconv"

	. "github.com/majewsky/gg/option"
	"github.com/sapcc/go-api-declarations/cadf"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/must"

	"github.com/sapcc/limes/internal/db"
)

// maxQuotaEventTarget renders a cadf.Event.Target for a max_quota change event.
type maxQuotaEventTarget struct {
	DomainID        string
	DomainName      string
	ProjectID       string
	ProjectName     string
	ServiceType     limes.ServiceType
	ResourceName    limesresources.ResourceName
	RequestedChange maxQuotaChange
}

type maxQuotaChange struct {
	OldValue Option[uint64] `json:"oldMaxQuota"`
	NewValue Option[uint64] `json:"newMaxQuota"`
}

// Render implements the audittools.Target interface.
func (t maxQuotaEventTarget) Render() cadf.Resource {
	return cadf.Resource{
		TypeURI:     fmt.Sprintf("service/%s/%s/max-quota", t.ServiceType, t.ResourceName),
		ID:          t.ProjectID,
		DomainID:    t.DomainID,
		DomainName:  t.DomainName,
		ProjectID:   t.ProjectID,
		ProjectName: t.ProjectName,
		Attachments: []cadf.Attachment{
			must.Return(cadf.NewJSONAttachment("payload", t.RequestedChange)),
		},
	}
}

// rateLimitEventTarget contains the structure for rendering a cadf.Event.Target for
// changes regarding rate limits
type rateLimitEventTarget struct {
	DomainID    string
	DomainName  string
	ProjectID   string
	ProjectName string
	ServiceType limes.ServiceType
	Name        limesrates.RateName
	Payload     rateLimitChange
}

// rateLimitChange appears in type rateLimitEventTarget.
type rateLimitChange struct {
	Unit         limes.Unit        `json:"unit,omitempty"`
	OldLimit     uint64            `json:"oldLimit"`
	NewLimit     uint64            `json:"newLimit"`
	OldWindow    limesrates.Window `json:"oldWindow"`
	NewWindow    limesrates.Window `json:"newWindow"`
	RejectReason string            `json:"rejectReason,omitempty"`
}

// Render implements the audittools.Target interface.
func (t rateLimitEventTarget) Render() cadf.Resource {
	return cadf.Resource{
		TypeURI:     fmt.Sprintf("service/%s/%s/rates", t.ServiceType, t.Name),
		ID:          t.ProjectID,
		DomainID:    t.DomainID,
		DomainName:  t.DomainName,
		ProjectID:   t.ProjectID,
		ProjectName: t.ProjectName,
		Attachments: []cadf.Attachment{
			must.Return(cadf.NewJSONAttachment("payload", t.Payload)),
		},
	}
}

// commitmentEventTarget contains the structure for rendering a cadf.Event.Target for
// changes regarding commitments.
type commitmentEventTarget struct {
	DomainID        string
	DomainName      string
	ProjectID       string
	ProjectName     string
	Commitments     []limesresources.Commitment // must have at least one entry
	WorkflowContext Option[db.CommitmentWorkflowContext]
}

// Render implements the audittools.Target interface.
func (t commitmentEventTarget) Render() cadf.Resource {
	if len(t.Commitments) == 0 {
		panic("commitmentEventTarget must contain at least one commitment")
	}
	res := cadf.Resource{
		TypeURI:     "service/resources/commitment",
		ID:          strconv.FormatInt(t.Commitments[0].ID, 10),
		DomainID:    t.DomainID,
		DomainName:  t.DomainName,
		ProjectID:   t.ProjectID,
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
