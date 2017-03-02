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

package drivers

import "github.com/sapcc/limes/pkg/limes"

//Driver is an interface that wraps the queries to backend services required by
//any collectors, in order to make it easy to swap out the driver
//implementation for a mock during unit tests.
type Driver interface {
	/********** Keystone (Identity) **********/
	ListDomains() ([]KeystoneDomain, error)
	ListProjects(domainUUID string) ([]KeystoneProject, error)
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

//This is the type that implements the Driver interface by actually calling out
//to OpenStack. The interface implementations are in the other source files in
//this module.
type realDriver struct {
	Cluster *limes.Cluster
}

//NewDriver instantiates a Driver for the given Cluster.
func NewDriver(c *limes.Cluster) Driver {
	return realDriver{c}
}
