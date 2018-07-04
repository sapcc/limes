package reports

import (
	"testing"

	th "github.com/gophercloud/gophercloud/testhelper"
	"github.com/sapcc/limes/pkg/limes"
)

var sharedServicesJSON = `
	[
		{
			"type": "shared",
			"area": "shared",
			"resources": [
				{
					"name": "capacity",
					"unit": "B",
					"quota": 10,
					"usage": 2
				},
				{
					"name": "things",
					"quota": 10,
					"usage": 2
				}
			],
			"scraped_at": 22
		}
	]
`

var resourcesJSON = `
 [
		{
			"name": "capacity",
			"unit": "B",
			"quota": 10,
			"usage": 2
		},
		{
			"name": "things",
			"quota": 10,
			"usage": 2
		}
	]
`

var resources = &ProjectResources{
	"capacity": &ProjectResource{
		ResourceInfo: limes.ResourceInfo{
			Name: "capacity",
			Unit: limes.UnitBytes,
		},
		Quota: 10,
		Usage: 2,
	},
	"things": &ProjectResource{
		ResourceInfo: limes.ResourceInfo{
			Name: "things",
		},
		Quota: 10,
		Usage: 2,
	},
}

var sharedServices = &ProjectServices{
	"shared": &ProjectService{
		ServiceInfo: limes.ServiceInfo{
			Type: "shared",
			Area: "shared",
		},
		Resources: *resources,
		ScrapedAt: 22,
	},
}

func TestProjectServicesMarshall(t *testing.T) {
	th.CheckJSONEquals(t, sharedServicesJSON, sharedServices)
}

func TestProjectServicesUnmarshall(t *testing.T) {
	actual := &ProjectServices{}
	err := actual.UnmarshalJSON([]byte(sharedServicesJSON))
	th.AssertNoErr(t, err)
	th.CheckDeepEquals(t, sharedServices, actual)
}

func TestProjectResourcesMarshall(t *testing.T) {
	th.CheckJSONEquals(t, sharedServicesJSON, sharedServices)
}

func TestProjectResourcesUnmarshall(t *testing.T) {
	actual := &ProjectServices{}
	err := actual.UnmarshalJSON([]byte(sharedServicesJSON))
	th.AssertNoErr(t, err)
	th.CheckDeepEquals(t, sharedServices, actual)
}
