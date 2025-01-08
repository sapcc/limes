/*******************************************************************************
*
* Copyright 2025 SAP SE
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

// Package liquidapi provides a runtime library for servers and clients implementing the LIQUID protocol:
// <https://pkg.go.dev/github.com/sapcc/go-api-declarations/liquid>
//
//   - func Run() provides a full-featured runtime that handles OpenStack credentials, authorization, and more.
//   - type Client is a specialized gophercloud.ServiceClient for use in Limes and limesctl.
//   - The other functions in this package contain various numeric algorithms that are useful for LIQUID implementations.
package liquidapi
