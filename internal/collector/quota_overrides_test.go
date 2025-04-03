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
	"os"
	"path/filepath"
	"testing"

	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/jobloop"

	"github.com/sapcc/limes/internal/plugins"
	"github.com/sapcc/limes/internal/test"
)

func TestApplyQuotaOverrides(t *testing.T) {
	// setup enough to have fully populated project_services and project_resources
	s := test.NewSetup(t,
		test.WithConfig(testScrapeBasicConfigYAML),
	)
	prepareDomainsAndProjectsForScrape(t, s)

	// the Scrape job needs a report that at least satisfies the topology constraints
	s.Cluster.QuotaPlugins["unittest"].(*plugins.LiquidQuotaPlugin).LiquidClient.(*test.MockLiquidClient).SetUsageReport(liquid.ServiceUsageReport{
		InfoVersion: 1,
		Resources: map[liquid.ResourceName]*liquid.ResourceUsageReport{
			"capacity": {
				Quota: pointerTo(int64(100)),
				PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceUsageReport{
					"az-one": {},
					"az-two": {},
				},
			},
			"things": {
				Quota: pointerTo(int64(42)),
				PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceUsageReport{
					"any": {},
				},
			},
		},
	})

	c := getCollector(t, s)
	scrapeJob := c.ResourceScrapeJob(s.Registry)
	withLabel := jobloop.WithLabel("service_type", "unittest")
	mustT(t, scrapeJob.ProcessOne(s.Ctx, withLabel))
	mustT(t, scrapeJob.ProcessOne(s.Ctx, withLabel)) // twice because there are two projects

	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.Ignore()
	job := c.ApplyQuotaOverridesJob(s.Registry)

	configPath := filepath.Join(t.TempDir(), "quota-overrides.json")
	t.Setenv("LIMES_QUOTA_OVERRIDES_PATH", configPath)

	// test applying some quota overrides
	buf := `{
		"germany": {
			"berlin": { "unittest": { "capacity": "10 B", "things": 1000 } }
		}
	}`
	mustT(t, os.WriteFile(configPath, []byte(buf), 0666))
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`
		UPDATE project_resources SET override_quota_from_config = 10 WHERE id = 1 AND service_id = 1 AND name = 'capacity';
		UPDATE project_resources SET override_quota_from_config = 1000 WHERE id = 2 AND service_id = 1 AND name = 'things';
	`)

	// test changing and removing quota overrides
	buf = `{
		"germany": {
			"berlin": { "unittest": { "capacity": "15 B" } },
			"dresden": { "unittest": { "capacity": "20 B" } }
		}
	}`
	mustT(t, os.WriteFile(configPath, []byte(buf), 0666))
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`
		UPDATE project_resources SET override_quota_from_config = 15 WHERE id = 1 AND service_id = 1 AND name = 'capacity';
		UPDATE project_resources SET override_quota_from_config = NULL WHERE id = 2 AND service_id = 1 AND name = 'things';
		UPDATE project_resources SET override_quota_from_config = 20 WHERE id = 3 AND service_id = 2 AND name = 'capacity';
	`)

	// test quota overrides referring to nonexistent domains and projects (should be ignored without error)
	buf = `{
		"france": {
			"paris": { "unittest": { "capacity": "42 B" } }
		},
		"germany": {
			"berlin": { "unittest": { "capacity": "15 B" } },
			"bremen": { "unittest": { "capacity": "42 B" } },
			"dresden": { "unittest": { "capacity": "20 B" } }
		}
	}`
	mustT(t, os.WriteFile(configPath, []byte(buf), 0666))
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEmpty()
}
