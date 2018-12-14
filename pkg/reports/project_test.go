package reports

import (
	"testing"

	th "github.com/gophercloud/gophercloud/testhelper"
	"github.com/sapcc/limes/pkg/core"
)

var projectServicesMockJSON = `
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

var projectResourcesMockJSON = `
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

var projectMockResources = &ProjectResources{
	"capacity": &ProjectResource{
		ResourceInfo: core.ResourceInfo{
			Name: "capacity",
			Unit: core.UnitBytes,
		},
		Quota: 10,
		Usage: 2,
	},
	"things": &ProjectResource{
		ResourceInfo: core.ResourceInfo{
			Name: "things",
		},
		Quota: 10,
		Usage: 2,
	},
}

var projectMockServices = &ProjectServices{
	"shared": &ProjectService{
		ServiceInfo: core.ServiceInfo{
			Type: "shared",
			Area: "shared",
		},
		Resources: *projectMockResources,
		ScrapedAt: p2i64(22),
	},
}

func TestProjectServicesMarshall(t *testing.T) {
	th.CheckJSONEquals(t, projectServicesMockJSON, projectMockServices)
}

func TestProjectServicesUnmarshall(t *testing.T) {
	actual := &ProjectServices{}
	err := actual.UnmarshalJSON([]byte(projectServicesMockJSON))
	th.AssertNoErr(t, err)
	th.CheckDeepEquals(t, projectMockServices, actual)
}

func TestProjectResourcesMarshall(t *testing.T) {
	th.CheckJSONEquals(t, projectResourcesMockJSON, projectMockResources)
}

func TestProjectResourcesUnmarshall(t *testing.T) {
	actual := &ProjectResources{}
	err := actual.UnmarshalJSON([]byte(projectResourcesMockJSON))
	th.AssertNoErr(t, err)
	th.CheckDeepEquals(t, projectMockResources, actual)
}
