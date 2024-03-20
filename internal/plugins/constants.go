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

package plugins

// This is a list of all *stable* provisioning states of an Ironic node which will
// cause that node to not be considered when counting capacity.
//
// Reference: https://github.com/openstack/ironic/blob/master/ironic/common/states.py
var isAvailableProvisionState = map[string]bool{
	"enroll":     false,
	"manageable": false,
	"available":  true,
	"active":     true,
	"error":      true, // occurs during delete or rebuild, so node was active before
	"rescue":     true,
}
