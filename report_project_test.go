package limes

import (
	"testing"

	th "github.com/gophercloud/gophercloud/testhelper"
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
					"usable_quota": 11,
					"usage": 2
				},
				{
					"name": "things",
					"quota": 10,
					"usable_quota": 10,
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
			"usable_quota": 11,
			"usage": 2
		},
		{
			"name": "things",
			"quota": 10,
			"usable_quota": 10,
			"usage": 2
		}
	]
`

var projectServicesRateLimitMockJSON = `
	[
		{
			"type": "shared",
			"area": "shared",
			"resources": [],
			"rates": [
				{
					"target_type_uri": "services/swift/account",
					"actions": [
						{
							"name": "create",
							"limit": 10,
							"unit": "r/m"
						}
					]
				},
				{
					"target_type_uri": "services/swift/account/container/object",
					"actions": [
						{
							"name": "create",
							"limit": 1000,
							"unit": "r/s"
						},
						{
							"name": "delete",
							"limit": 1000,
							"unit": "r/s"
						}
					]
				}
			],
			"scraped_at": 22
		}
	]
`

var projectServicesRateLimitDeviatingFromDefaultsMockJSON = `
	[
		{
			"type": "shared",
			"area": "shared",
			"resources": [],
			"rates": [
				{
					"target_type_uri": "services/swift/account",
					"actions": [
						{
							"name": "create",
							"limit": 10,
							"unit": "r/m",
							"default_limit": 5,
							"default_unit": "r/m"
						}
					]
				},
				{
					"target_type_uri": "services/swift/account/container/object",
					"actions": [
						{
							"name": "create",
							"limit": 1000,
							"unit": "r/s",
							"default_limit": 500,
							"default_unit": "r/s"
						},
						{
							"name": "delete",
							"limit": 1000,
							"unit": "r/s",
							"default_limit": 500,
							"default_unit": "r/s"
						}
					]
				}
			],
			"scraped_at": 22
		}
	]
`

var projectMockResources = &ProjectResourceReports{
	"capacity": &ProjectResourceReport{
		ResourceInfo: ResourceInfo{
			Name: "capacity",
			Unit: UnitBytes,
		},
		Quota:       10,
		UsableQuota: 11,
		Usage:       2,
	},
	"things": &ProjectResourceReport{
		ResourceInfo: ResourceInfo{
			Name: "things",
		},
		Quota:       10,
		UsableQuota: 10,
		Usage:       2,
	},
}

var projectMockServices = &ProjectServiceReports{
	"shared": &ProjectServiceReport{
		ServiceInfo: ServiceInfo{
			Type: "shared",
			Area: "shared",
		},
		Resources: *projectMockResources,
		ScrapedAt: p2i64(22),
	},
}

var projectMockServicesRateLimit = &ProjectServiceReports{
	"shared": &ProjectServiceReport{
		ServiceInfo: ServiceInfo{
			Type: "shared",
			Area: "shared",
		},
		Resources: ProjectResourceReports{},
		Rates: ProjectRateLimitReports{
			"services/swift/account/container/object": &ProjectRateLimitReport{
				TargetTypeURI: "services/swift/account/container/object",
				Actions: ProjectRateLimitActionReports{
					"create": &ProjectRateLimitActionReport{
						Name:  "create",
						Limit: 1000,
						Unit:  UnitRequestsPerSeconds,
					},
					"delete": &ProjectRateLimitActionReport{
						Name:  "delete",
						Limit: 1000,
						Unit:  UnitRequestsPerSeconds,
					},
				},
			},
			"services/swift/account": &ProjectRateLimitReport{
				TargetTypeURI: "services/swift/account",
				Actions: ProjectRateLimitActionReports{
					"create": &ProjectRateLimitActionReport{
						Name:  "create",
						Limit: 10,
						Unit:  UnitRequestsPerMinute,
					},
				},
			},
		},
		ScrapedAt: p2i64(22),
	},
}

var projectMockServicesRateLimitDeviatingFromDefaults = &ProjectServiceReports{
	"shared": &ProjectServiceReport{
		ServiceInfo: ServiceInfo{
			Type: "shared",
			Area: "shared",
		},
		Resources: ProjectResourceReports{},
		Rates: ProjectRateLimitReports{
			"services/swift/account/container/object": &ProjectRateLimitReport{
				TargetTypeURI: "services/swift/account/container/object",
				Actions: ProjectRateLimitActionReports{
					"create": &ProjectRateLimitActionReport{
						Name:         "create",
						Limit:        1000,
						Unit:         UnitRequestsPerSeconds,
						DefaultLimit: 500,
						DefaultUnit:  UnitRequestsPerSeconds,
					},
					"delete": &ProjectRateLimitActionReport{
						Name:         "delete",
						Limit:        1000,
						Unit:         UnitRequestsPerSeconds,
						DefaultLimit: 500,
						DefaultUnit:  UnitRequestsPerSeconds,
					},
				},
			},
			"services/swift/account": &ProjectRateLimitReport{
				TargetTypeURI: "services/swift/account",
				Actions: ProjectRateLimitActionReports{
					"create": &ProjectRateLimitActionReport{
						Name:         "create",
						Limit:        10,
						Unit:         UnitRequestsPerMinute,
						DefaultLimit: 5,
						DefaultUnit:  UnitRequestsPerMinute,
					},
				},
			},
		},
		ScrapedAt: p2i64(22),
	},
}

func TestProjectServicesMarshall(t *testing.T) {
	th.CheckJSONEquals(t, projectServicesMockJSON, projectMockServices)
}

func TestProjectServicesUnmarshall(t *testing.T) {
	actual := &ProjectServiceReports{}
	err := actual.UnmarshalJSON([]byte(projectServicesMockJSON))
	th.AssertNoErr(t, err)
	th.CheckDeepEquals(t, projectMockServices, actual)
}

func TestProjectResourcesMarshall(t *testing.T) {
	th.CheckJSONEquals(t, projectResourcesMockJSON, projectMockResources)
}

func TestProjectResourcesUnmarshall(t *testing.T) {
	actual := &ProjectResourceReports{}
	err := actual.UnmarshalJSON([]byte(projectResourcesMockJSON))
	th.AssertNoErr(t, err)
	th.CheckDeepEquals(t, projectMockResources, actual)
}

func TestProjectServicesRateLimitMarshall(t *testing.T) {
	th.CheckJSONEquals(t, projectServicesRateLimitMockJSON, projectMockServicesRateLimit)
}

func TestProjectServicesRateLimitUnmarshall(t *testing.T) {
	actual := &ProjectServiceReports{}
	err := actual.UnmarshalJSON([]byte(projectServicesRateLimitMockJSON))
	th.AssertNoErr(t, err)
	th.CheckDeepEquals(t, projectMockServicesRateLimit, actual)
}

func TestProjectServicesRateLimitDeviatingFromDefaultsMarshall(t *testing.T) {
	th.CheckJSONEquals(t, projectServicesRateLimitDeviatingFromDefaultsMockJSON, projectMockServicesRateLimitDeviatingFromDefaults)
}

func TestProjectServicesRateLimitDeviatingFromDefaultsUnmarshall(t *testing.T) {
	actual := &ProjectServiceReports{}
	err := actual.UnmarshalJSON([]byte(projectServicesRateLimitDeviatingFromDefaultsMockJSON))
	th.AssertNoErr(t, err)
	th.CheckDeepEquals(t, projectMockServicesRateLimitDeviatingFromDefaults, actual)
}
