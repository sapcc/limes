/*******************************************************************************
*
* Copyright 2022 SAP SE
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

package limesresources

import (
	"fmt"
	"math"

	"github.com/sapcc/go-api-declarations/limes"
)

// ResourceInfo contains the metadata for a resource (i.e. some thing for which
// quota and usage values can be retrieved from a backend service).
type ResourceInfo struct {
	Name string     `json:"name"`
	Unit limes.Unit `json:"unit,omitempty"`
	//Category is an optional hint that UIs can use to group resources of one
	//service into subgroups. If it is used, it should be set on all
	//ResourceInfos reported by the same QuotaPlugin.
	Category string `json:"category,omitempty"`
	//If AutoApproveInitialQuota is non-zero, when a new project is scraped for
	//the first time, a backend quota equal to this value will be approved
	//automatically (i.e. Quota will be set equal to BackendQuota).
	AutoApproveInitialQuota uint64 `json:"-"`
	//If NoQuota is true, quota is not tracked at all for this resource. The
	//resource will only report usage. This field is not shown in API responses.
	//Check `res.Quota == nil` instead.
	NoQuota bool `json:"-"`
	//ContainedIn is an optional hint that UIs can use to group resources. If non-empty,
	//this resource is semantically contained within the resource with that name
	//in the same service.
	ContainedIn string `json:"contained_in,omitempty"`
}

// BurstingMultiplier is a multiplier for quota bursting.
type BurstingMultiplier float64

// ApplyTo returns the bursted backend quota for the given approved quota.
func (m BurstingMultiplier) ApplyTo(quota uint64, qdModel QuotaDistributionModel) uint64 {
	switch qdModel {
	case CentralizedQuotaDistribution:
		return quota
	case HierarchicalQuotaDistribution:
		return uint64(math.Floor((1 + float64(m)) * float64(quota)))
	default:
		panic(fmt.Sprintf("unknown quota distribution model: %q", string(qdModel)))
	}
}

// ScalingBehavior appears in type DomainResourceReport and type
// ProjectResourceReport and describes the scaling behavior of a single
// resource.
type ScalingBehavior struct {
	ScalesWithResourceName string  `json:"resource_name"`
	ScalesWithServiceType  string  `json:"service_type"`
	ScalingFactor          float64 `json:"factor"`
}

// QuotaDistributionModel is an enum.
type QuotaDistributionModel string

const (
	// HierarchicalQuotaDistribution is the default QuotaDistributionModel,
	// wherein quota is distributed to domains by the cloud admins, and then the
	// projects by the domain admins. Domains and projects start out at zero
	// quota.
	HierarchicalQuotaDistribution QuotaDistributionModel = "hierarchical"
	// CentralizedQuotaDistribution is an alternative QuotaDistributionModel,
	// wherein quota is directly given to projects by the cloud admins. Projects
	// start out at a generous default quota as configured by the Limes operator.
	// The domain quota cannot be edited and is always reported equal to the
	// projects quota.
	CentralizedQuotaDistribution QuotaDistributionModel = "centralized"
)

// CommitmentConfiguration describes how commitments are configured for a given resource.
//
// This appears as a field on resource reports, if the respective resource allows commitments.
type CommitmentConfiguration struct {
	// Allowed durations for commitments on this resource.
	Durations []CommitmentDuration `json:"durations"`
}
