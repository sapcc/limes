/*******************************************************************************
*
* Copyright 2017 SAP SE
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

import policy "github.com/databus23/goslo.policy"

//Driver is an interface that wraps any queries and requests to backend
//services, in order to make it easy to swap out the driver implementation for
//a mock during unit tests.
type Driver interface {
	//Return the Cluster that this Driver instance operates on. This is useful
	//because it means we just have to pass around the Driver instance in
	//function calls, instead of both the Driver and the Cluster.
	Cluster() *ClusterConfiguration
	/********** Keystone (Identity) **********/
	ListDomains() ([]KeystoneDomain, error)
	ListProjects(domainUUID string) ([]KeystoneProject, error)
	CheckUserPermission(token, rule string, enforcer *policy.Enforcer, requestParams map[string]string) (bool, error)
	/********** Nova (Compute) **********/
	CheckCompute(projectUUID string) (ComputeData, error)
	SetComputeQuota(projectUUID string, data ComputeData) error //disregards .Usage fields
}

//KeystoneDomain describes the basic attributes of a Keystone domain.
type KeystoneDomain struct {
	UUID string `json:"id"`
	Name string `json:"name"`
}

//KeystoneProject describes the basic attributes of a Keystone project.
type KeystoneProject struct {
	UUID string `json:"id"`
	Name string `json:"name"`
}

//ResourceData contains quota and usage data for a single resource.
type ResourceData struct {
	Quota int64 //negative values indicate infinite quota
	Usage uint64
}

//ComputeData contains quota or usage values for a project's compute resources.
type ComputeData struct {
	Cores     ResourceData
	Instances ResourceData
	RAM       ResourceData
}
