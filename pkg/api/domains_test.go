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

package api

import (
	"fmt"
	"testing"

	"github.com/sapcc/limes/pkg/test"
)

func Test_DomainOperations(t *testing.T) {
	driver, router := testSetup(t)

	domainUUID := driver.StaticDomains[0].UUID

	//check GetDomain
	test.APIRequest{
		Method:           "GET",
		Path:             fmt.Sprintf("/v1/domains/%s", domainUUID),
		ExpectStatusCode: 200,
		ExpectJSON:       "./fixtures/get-domain.json",
	}.Check(t, router)

	//check ListDomains
	test.APIRequest{
		Method:           "GET",
		Path:             "/v1/domains",
		ExpectStatusCode: 200,
		ExpectJSON:       "./fixtures/list-domains.json",
	}.Check(t, router)
}
