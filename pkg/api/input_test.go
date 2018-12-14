package api

import (
	"testing"

	th "github.com/gophercloud/gophercloud/testhelper"
	"github.com/sapcc/limes/pkg/core"
)

var quotas = ServiceQuotas{
	"volumev2": ResourceQuotas{
		"capacity": core.ValueWithUnit{
			Value: 1024,
			Unit:  core.UnitBytes,
		},
		"volumes": core.ValueWithUnit{
			Value: 16,
			Unit:  core.UnitNone,
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

func TestServiceQuotasMarshall(t *testing.T) {
	th.CheckJSONEquals(t, quotaJSON, quotas)
}

func TestServiceQuotasUnmarshall(t *testing.T) {
	actual := ServiceQuotas{}
	err := actual.UnmarshalJSON([]byte(quotaJSON))
	th.AssertNoErr(t, err)
	th.CheckDeepEquals(t, quotas, actual)
}
