// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package limesresources

import (
	"github.com/sapcc/go-api-declarations/limes"
)

// ResourceName identifies a resource within a service. This type is used to distinguish
// resource names from other types of string values in function signatures.
//
// This type is legally distinct from liquid.ResourceName: Within Limes,
// LIQUID-level resource names and API-level resource names are separated by
// configurable renaming rules, so we are using separate types to enforce that
// this conversion takes place.
type ResourceName string

// ResourceInfo contains the metadata for a resource (i.e. some thing for which
// quota and usage values can be retrieved from a backend service).
type ResourceInfo struct {
	Name ResourceName `json:"name"`
	Unit limes.Unit   `json:"unit,omitempty"`
	// Category is an optional hint that UIs can use to group resources of one
	// service into subgroups. If it is used, it should be set on all
	// ResourceInfos reported by the same QuotaPlugin.
	Category string `json:"category,omitempty"`
	// If NoQuota is true, quota is not tracked at all for this resource. The
	// resource will only report usage. This field is not shown in API responses.
	// Check `res.Quota == nil` instead.
	NoQuota bool `json:"-"`
	// ContainedIn is an optional hint that UIs can use to group resources. If non-empty,
	// this resource is semantically contained within the resource with that name
	// in the same service.
	ContainedIn ResourceName `json:"contained_in,omitempty"`
}

// QuotaDistributionModel is an enum.
type QuotaDistributionModel string

const (
	// HierarchicalQuotaDistribution is the default QuotaDistributionModel,
	// wherein quota is distributed to domains by the cloud admins, and then the
	// projects by the domain admins. Domains and projects start out at zero
	// quota.
	HierarchicalQuotaDistribution QuotaDistributionModel = "hierarchical"
	// AutogrowQuotaDistribution is an alternative QuotaDistributionModel,
	// wherein project quota is automatically distributed ("auto") such that:
	// 1. all active commitments and usage are represented in their respective project quota,
	// 2. there is some space beyond the current commitment/usage ("grow").
	//
	// Domain quota is irrelevant under this model. Project quota never sinks
	// below a certain value (the "base quota") unless capacity is exhausted.
	AutogrowQuotaDistribution QuotaDistributionModel = "autogrow"
)

// CommitmentConfiguration describes how commitments are configured for a given resource.
//
// This appears as a field on resource reports, if the respective resource allows commitments.
type CommitmentConfiguration struct {
	// Allowed durations for commitments on this resource.
	Durations []CommitmentDuration `json:"durations"`
	// If shown, commitments must be created with `confirm_by` at or after this timestamp.
	MinConfirmBy *limes.UnixEncodedTime `json:"min_confirm_by,omitempty"`
}
