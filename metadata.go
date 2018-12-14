/*******************************************************************************
*
* Copyright 2018 SAP SE
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

package limes

import "math"

//ResourceInfo contains the metadata for a resource (i.e. some thing for which
//quota and usage values can be retrieved from a backend service).
type ResourceInfo struct {
	Name string `json:"name"`
	Unit Unit   `json:"unit,omitempty"`
	//Category is an optional hint that UIs can use to group resources of one
	//service into subgroups. If it is used, it should be set on all
	//ResourceInfos reported by the same QuotaPlugin.
	Category string `json:"category,omitempty"`
	//If AutoApproveInitialQuota is non-zero, when a new project is scraped for
	//the first time, a backend quota equal to this value will be approved
	//automatically (i.e. Quota will be set equal to BackendQuota).
	AutoApproveInitialQuota uint64 `json:"-"`
	//If ExternallyManaged is true, quota cannot be set via the API. The quota
	//value reported by the QuotaPlugin is always authoritative.
	ExternallyManaged bool `json:"externally_managed,omitempty"`
}

//ServiceInfo contains the metadata for a backend service.
type ServiceInfo struct {
	//Type returns the service type that the backend service for this
	//plugin implements. This string must be identical to the type string from
	//the Keystone service catalog.
	Type string `json:"type"`
	//ProductName returns the name of the product that is the reference
	//implementation for this service. For example, ProductName = "nova" for
	//Type = "compute".
	ProductName string `json:"-"`
	//Area is a hint that UIs can use to group similar services.
	Area string `json:"area"`
}

//BurstingMultiplier is a multiplier for quota bursting.
type BurstingMultiplier float64

//ApplyTo returns the bursted backend quota for the given approved quota.
func (m BurstingMultiplier) ApplyTo(quota uint64) uint64 {
	return uint64(math.Floor((1 + float64(m)) * float64(quota)))
}

//ScalingBehavior appears in type DomainResourceReport and type
//ProjectResourceReport and describes the scaling behavior of a single
//resource.
type ScalingBehavior struct {
	ScalesWithResourceName string  `json:"service_type"`
	ScalesWithServiceType  string  `json:"resource_name"`
	ScalingFactor          float64 `json:"factor"`
}
