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

import (
	"testing"

	th "github.com/gophercloud/gophercloud/testhelper"
)

var quotas = QuotaRequest{
	"volumev2": ServiceQuotaRequest{
		Resources: ResourceQuotaRequest{
			"capacity": {
				Value: 1024,
				Unit:  UnitBytes,
			},
			"volumes": {
				Value: 16,
				Unit:  UnitNone,
			},
		},
		Rates: map[string]RateLimitRequest{},
	},
}

var quotaJSON = `
	[
		{
			"type": "volumev2",
			"resources": [
				{
					"name": "capacity",
					"quota": 1024,
					"unit": "B"
				},
				{
					"name": "volumes",
					"quota": 16,
					"unit": ""
				}
			],
			"rates": []
		}
	]
`

var rateLimits = QuotaRequest{
	"object-store": ServiceQuotaRequest{
		Rates: map[string]RateLimitRequest{
			"object/account/container:create": {Limit: 1000, Window: 1 * WindowSeconds},
			"object/account/container:delete": {Limit: 100, Window: 1 * WindowSeconds},
		},
		Resources: ResourceQuotaRequest{},
	},
}

var rateLimitJSON = `
	[
		{
			"type": "object-store",
			"resources": [],
			"rates": [
				{
					"name": "object/account/container:create",
					"limit": 1000,
					"window": "1s"
				},
				{
					"name": "object/account/container:delete",
					"limit": 100,
					"window": "1s"
				}
			]
		}
	]
`

func TestQuotaRequestMarshall(t *testing.T) {
	th.CheckJSONEquals(t, quotaJSON, quotas)
}

func TestQuotaRequestUnmarshall(t *testing.T) {
	actual := QuotaRequest{}
	err := actual.UnmarshalJSON([]byte(quotaJSON))
	th.AssertNoErr(t, err)
	th.CheckDeepEquals(t, quotas, actual)
}

func TestQuotaRateLimitMarshall(t *testing.T) {
	th.CheckJSONEquals(t, rateLimitJSON, rateLimits)
}

func TestRateLimitRequestUnmarshall(t *testing.T) {
	actual := QuotaRequest{}
	err := actual.UnmarshalJSON([]byte(rateLimitJSON))
	th.AssertNoErr(t, err)
	th.CheckDeepEquals(t, rateLimits, actual)
}
