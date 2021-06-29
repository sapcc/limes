// Copyright 2018 SAP SE
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
					"name": "services/swift/account/container/object:create",
					"limit": 1000,
					"window": "1s"
				},
				{
					"name": "services/swift/account/container/object:delete",
					"limit": 1000,
					"window": "1s"
				},
				{
					"name": "services/swift/account:create",
					"limit": 10,
					"window": "1m"
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
					"name": "services/swift/account/container/object:create",
					"limit": 1000,
					"window": "1s",
					"default_limit": 500,
					"default_window": "1s"
				},
				{
					"name": "services/swift/account/container/object:delete",
					"limit": 1000,
					"window": "1s",
					"default_limit": 500,
					"default_window": "1s"
				},
				{
					"name": "services/swift/account:create",
					"limit": 10,
					"window": "1m",
					"default_limit": 5,
					"default_window": "1m"
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
		Quota:       p2u64(10),
		UsableQuota: p2u64(11),
		Usage:       2,
	},
	"things": &ProjectResourceReport{
		ResourceInfo: ResourceInfo{
			Name: "things",
		},
		Quota:       p2u64(10),
		UsableQuota: p2u64(10),
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
			"services/swift/account/container/object:create": {
				RateInfo: RateInfo{Name: "services/swift/account/container/object:create"},
				Limit:    1000,
				Window:   p2window(1 * WindowSeconds),
			},
			"services/swift/account/container/object:delete": {
				RateInfo: RateInfo{Name: "services/swift/account/container/object:delete"},
				Limit:    1000,
				Window:   p2window(1 * WindowSeconds),
			},
			"services/swift/account:create": {
				RateInfo: RateInfo{Name: "services/swift/account:create"},
				Limit:    10,
				Window:   p2window(1 * WindowMinutes),
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
			"services/swift/account/container/object:create": {
				RateInfo:      RateInfo{Name: "services/swift/account/container/object:create"},
				Limit:         1000,
				Window:        p2window(1 * WindowSeconds),
				DefaultLimit:  500,
				DefaultWindow: p2window(1 * WindowSeconds),
			},
			"services/swift/account/container/object:delete": {
				RateInfo:      RateInfo{Name: "services/swift/account/container/object:delete"},
				Limit:         1000,
				Window:        p2window(1 * WindowSeconds),
				DefaultLimit:  500,
				DefaultWindow: p2window(1 * WindowSeconds),
			},
			"services/swift/account:create": {
				RateInfo:      RateInfo{Name: "services/swift/account:create"},
				Limit:         10,
				Window:        p2window(1 * WindowMinutes),
				DefaultLimit:  5,
				DefaultWindow: p2window(1 * WindowMinutes),
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

func p2window(val Window) *Window {
	return &val
}
