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
		"capacity": ValueWithUnit{
			Value: 1024,
			Unit:  UnitBytes,
		},
		"volumes": ValueWithUnit{
			Value: 16,
			Unit:  UnitNone,
		},
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
