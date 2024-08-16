/*******************************************************************************
*
* Copyright 2024 SAP SE
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

package collector

import (
	"testing"

	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/jobloop"

	"github.com/sapcc/limes/internal/test"
)

func TestApplyQuotaOverrides(t *testing.T) {
	// setup enough to have fully populated project_services and project_resources
	s := test.NewSetup(t,
		test.WithConfig(testScrapeBasicConfigYAML),
	)
	prepareDomainsAndProjectsForScrape(t, s)

	c := getCollector(t, s)
	scrapeJob := c.ResourceScrapeJob(s.Registry)
	withLabel := jobloop.WithLabel("service_type", "unittest")
	mustT(t, scrapeJob.ProcessOne(s.Ctx, withLabel))
	mustT(t, scrapeJob.ProcessOne(s.Ctx, withLabel)) // twice because there are two projects

	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.Ignore()
	job := c.ApplyQuotaOverridesJob(s.Registry)

	// test applying some quota overrides
	s.Cluster.QuotaOverrides = map[string]map[string]map[limes.ServiceType]map[limesresources.ResourceName]uint64{
		"germany": {
			"berlin": {
				"unittest": {
					"capacity": 10,
					"things":   1000,
				},
			},
		},
	}
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`
		UPDATE project_resources SET override_quota_from_config = 10 WHERE id = 1 AND service_id = 1 AND name = 'capacity';
		UPDATE project_resources SET override_quota_from_config = 1000 WHERE id = 3 AND service_id = 1 AND name = 'things';
	`)

	// test changing and removing quota overrides
	s.Cluster.QuotaOverrides = map[string]map[string]map[limes.ServiceType]map[limesresources.ResourceName]uint64{
		"germany": {
			"berlin": {
				"unittest": {
					"capacity": 15,
				},
			},
			"dresden": {
				"unittest": {
					"capacity": 20,
				},
			},
		},
	}
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`
		UPDATE project_resources SET override_quota_from_config = 15 WHERE id = 1 AND service_id = 1 AND name = 'capacity';
		UPDATE project_resources SET override_quota_from_config = NULL WHERE id = 3 AND service_id = 1 AND name = 'things';
		UPDATE project_resources SET override_quota_from_config = 20 WHERE id = 4 AND service_id = 2 AND name = 'capacity';
	`)

	// test quota overrides referring to nonexistent projects (should be ignored without error)
	s.Cluster.QuotaOverrides["france"] = map[string]map[limes.ServiceType]map[limesresources.ResourceName]uint64{
		"paris": {
			"unittest": {
				"capacity": 42,
			},
		},
	}
	s.Cluster.QuotaOverrides["germany"]["bremen"] = map[limes.ServiceType]map[limesresources.ResourceName]uint64{
		"unittest": {
			"capacity": 42,
		},
	}
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEmpty()
}
