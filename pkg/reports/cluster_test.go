package reports

import (
	"testing"

	th "github.com/gophercloud/gophercloud/testhelper"
	"github.com/sapcc/limes/pkg/limes"
)

var clusterServicesMockJSON = `
	[
		{
			"type": "compute",
			"area": "compute",
			"resources": [
				{
					"name": "cores",
					"capacity": 500,
					"domains_quota": 200,
					"usage": 100
				},
				{
					"name": "ram",
					"unit": "MiB",
					"capacity": 204800,
					"domains_quota": 102400,
					"usage": 40800
				}
			],
			"max_scraped_at": 1539024049,
			"min_scraped_at": 1539023764
		}
	]
`

var clusterResourcesMockJSON = `
	[
		{
			"name": "cores",
			"capacity": 500,
			"domains_quota": 200,
			"usage": 100
		},
		{
			"name": "ram",
			"unit": "MiB",
			"capacity": 204800,
			"domains_quota": 102400,
			"usage": 40800
		}
	]
`

var clusterMockResources = &ClusterResources{
	"cores": &ClusterResource{
		ResourceInfo: limes.ResourceInfo{
			Name: "cores",
		},
		Capacity:     &coresCap,
		DomainsQuota: 200,
		Usage:        100,
	},
	"ram": &ClusterResource{
		ResourceInfo: limes.ResourceInfo{
			Name: "ram",
			Unit: limes.UnitMebibytes,
		},
		Capacity:     &ramCap,
		DomainsQuota: 102400,
		Usage:        40800,
	},
}

var coresCap uint64 = 500
var ramCap uint64 = 204800

var clusterMockServices = &ClusterServices{
	"compute": &ClusterService{
		ServiceInfo: limes.ServiceInfo{
			Type: "compute",
			Area: "compute",
		},
		Resources:    *clusterMockResources,
		MaxScrapedAt: p2i64(1539024049),
		MinScrapedAt: p2i64(1539023764),
	},
}

func p2i64(val int64) *int64 {
	return &val
}

func TestClusterServicesMarshal(t *testing.T) {
	th.CheckJSONEquals(t, clusterServicesMockJSON, clusterMockServices)
}

func TestClusterServicesUnmarshal(t *testing.T) {
	actual := &ClusterServices{}
	err := actual.UnmarshalJSON([]byte(clusterServicesMockJSON))
	th.AssertNoErr(t, err)
	th.CheckDeepEquals(t, clusterMockServices, actual)
}

func TestClusterResourcesMarshal(t *testing.T) {
	th.CheckJSONEquals(t, clusterResourcesMockJSON, clusterMockResources)
}

func TestClusterResourcesUnmarshal(t *testing.T) {
	actual := &ClusterResources{}
	err := actual.UnmarshalJSON([]byte(clusterResourcesMockJSON))
	th.AssertNoErr(t, err)
	th.CheckDeepEquals(t, clusterMockResources, actual)
}
