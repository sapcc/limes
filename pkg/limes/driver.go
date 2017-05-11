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

//Driver is an interface that wraps the authorization of the service user and
//queries to Keystone (i.e. all requests to backend services that are not
//performed by a limes.{Quota,Capacity}Plugin). It makes the service user
//connection available to limes.{Quota,Capacity}Plugin instances.  Because it
//is an interface, the real implementation can be mocked away in unit tests.
type Driver interface {
	//Return the Cluster that this Driver instance operates on. This is useful
	//because it means we just have to pass around the Driver instance in
	//function calls, instead of both the Driver and the Cluster.
	Cluster() *Cluster
	/********** requests to Keystone **********/
	ValidateToken(token string) (policy.Context, error)
}

////////////////////////////////////////////////////////////////////////////////

//This is the type that implements the Driver interface by actually
//calling out to OpenStack. It also manages the Keystone token that's required
//for all access to OpenStack APIs. The interface implementations are in the
//other source files in this module.
type realDriver struct {
	cluster *Cluster //need to use lowercase "cluster" because "Cluster" is already a method
}

//NewDriver instantiates a Driver for the given Cluster.
func NewDriver(cfg *Cluster) (Driver, error) {
	d := &realDriver{
		cluster: cfg,
	}

	return d, d.cluster.Connect()
}

//Cluster implements the Driver interface.
func (d *realDriver) Cluster() *Cluster {
	return d.cluster
}

//ValidateToken implements the Driver interface.
func (d realDriver) ValidateToken(token string) (policy.Context, error) {
	return d.Cluster().Config.ValidateToken(token)
}
