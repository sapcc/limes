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

package collector

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/limes/pkg/core"
	"github.com/sapcc/limes/pkg/test"
)

func keystoneTestCluster(t *testing.T) *core.Cluster {
	test.InitDatabase(t, nil)

	return &core.Cluster{
		ID:              "west",
		Config:          &core.ClusterConfiguration{Auth: &core.AuthParameters{}},
		ServiceTypes:    []string{"unshared", "shared"},
		DiscoveryPlugin: test.NewDiscoveryPlugin(),
		QuotaPlugins: map[string]core.QuotaPlugin{
			"shared":   test.NewPlugin("shared"),
			"unshared": test.NewPlugin("unshared"),
		},
		CapacityPlugins: map[string]core.CapacityPlugin{},
	}
}

func Test_ScanDomains(t *testing.T) {
	cluster := keystoneTestCluster(t)
	discovery := cluster.DiscoveryPlugin.(*test.DiscoveryPlugin)

	//construct expectation for return value
	var expectedNewDomains []string
	for _, domain := range discovery.StaticDomains {
		expectedNewDomains = append(expectedNewDomains, domain.UUID)
	}

	//add a quota constraint set; we're going to test if it's applied correctly
	pointerTo := func(x uint64) *uint64 { return &x }
	cluster.QuotaConstraints = &core.QuotaConstraintSet{
		Domains: map[string]core.QuotaConstraints{
			"germany": {
				"unshared": {
					"things":   {Minimum: pointerTo(10)},
					"capacity": {Minimum: pointerTo(20)},
				},
			},
		},
		Projects: map[string]map[string]core.QuotaConstraints{
			"germany": {
				"berlin": {
					"unshared": {
						"things": {Minimum: pointerTo(5)},
					},
					"shared": {
						"capacity": {Minimum: pointerTo(10)},
					},
				},
			},
		},
	}

	//first ScanDomains should discover the StaticDomains in the cluster,
	//and initialize domains, projects and project_services (project_resources
	//are then constructed by the scraper, and domain_services/domain_resources
	//are created when a cloud admin approves quota for the domain)
	//
	//This also tests that the quota constraint is applied correctly.
	actualNewDomains, err := ScanDomains(cluster, ScanDomainsOpts{})
	if err != nil {
		t.Errorf("ScanDomains #1 failed: %v", err)
	}
	sort.Strings(expectedNewDomains) //order does not matter
	sort.Strings(actualNewDomains)
	assert.DeepEqual(t, "new domains after ScanDomains #1", actualNewDomains, expectedNewDomains)
	test.AssertDBContent(t, "fixtures/scandomains1.sql")

	//first ScanDomains should not discover anything new
	actualNewDomains, err = ScanDomains(cluster, ScanDomainsOpts{})
	if err != nil {
		t.Errorf("ScanDomains #2 failed: %v", err)
	}
	assert.DeepEqual(t, "new domains after ScanDomains #2", actualNewDomains, []string(nil))
	test.AssertDBContent(t, "fixtures/scandomains1.sql")

	//add another project
	domainUUID := "uuid-for-france"
	discovery.StaticProjects[domainUUID] = append(discovery.StaticProjects[domainUUID],
		core.KeystoneProject{Name: "bordeaux", UUID: "uuid-for-bordeaux", ParentUUID: "uuid-for-france"},
	)

	//ScanDomains without ScanAllProjects should not see this new project
	actualNewDomains, err = ScanDomains(cluster, ScanDomainsOpts{})
	if err != nil {
		t.Errorf("ScanDomains #3 failed: %v", err)
	}
	assert.DeepEqual(t, "new domains after ScanDomains #3", actualNewDomains, []string(nil))
	test.AssertDBContent(t, "fixtures/scandomains1.sql")

	//ScanDomains with ScanAllProjects should discover the new project
	actualNewDomains, err = ScanDomains(cluster, ScanDomainsOpts{ScanAllProjects: true})
	if err != nil {
		t.Errorf("ScanDomains #4 failed: %v", err)
	}
	assert.DeepEqual(t, "new domains after ScanDomains #4", actualNewDomains, []string(nil))
	test.AssertDBContent(t, "fixtures/scandomains2.sql")

	//remove the project again
	discovery.StaticProjects[domainUUID] = discovery.StaticProjects[domainUUID][0:1]

	//ScanDomains without ScanAllProjects should not notice anything
	actualNewDomains, err = ScanDomains(cluster, ScanDomainsOpts{})
	if err != nil {
		t.Errorf("ScanDomains #5 failed: %v", err)
	}
	assert.DeepEqual(t, "new domains after ScanDomains #5", actualNewDomains, []string(nil))
	test.AssertDBContent(t, "fixtures/scandomains2.sql")

	//ScanDomains with ScanAllProjects should notice the deleted project and cleanup its records
	actualNewDomains, err = ScanDomains(cluster, ScanDomainsOpts{ScanAllProjects: true})
	if err != nil {
		t.Errorf("ScanDomains #6 failed: %v", err)
	}
	assert.DeepEqual(t, "new domains after ScanDomains #6", actualNewDomains, []string(nil))
	test.AssertDBContent(t, "fixtures/scandomains1.sql")

	//remove a whole domain
	discovery.StaticDomains = discovery.StaticDomains[0:1]

	//ScanDomains should notice the deleted domain and cleanup its records and also its projects
	actualNewDomains, err = ScanDomains(cluster, ScanDomainsOpts{})
	if err != nil {
		t.Errorf("ScanDomains #7 failed: %v", err)
	}
	assert.DeepEqual(t, "new domains after ScanDomains #7", actualNewDomains, []string(nil))
	test.AssertDBContent(t, "fixtures/scandomains3.sql")

	//rename a domain and a project
	discovery.StaticDomains[0].Name = "germany-changed"
	discovery.StaticProjects["uuid-for-germany"][0].Name = "berlin-changed"

	//ScanDomains should notice the changed names and update the domain/project records accordingly
	actualNewDomains, err = ScanDomains(cluster, ScanDomainsOpts{ScanAllProjects: true})
	if err != nil {
		t.Errorf("ScanDomains #8 failed: %v", err)
	}
	assert.DeepEqual(t, "new domains after ScanDomains #8", actualNewDomains, []string(nil))
	test.AssertDBContent(t, "fixtures/scandomains4.sql")
}

func Test_listDomainsFiltered(t *testing.T) {
	cluster := &core.Cluster{
		DiscoveryPlugin: &test.DiscoveryPlugin{
			StaticDomains: []core.KeystoneDomain{
				{Name: "bar1"},
				{Name: "bar2"},
				{Name: "foo1"},
				{Name: "foo2"},
			},
		},
		Config: &core.ClusterConfiguration{
			Auth: &core.AuthParameters{},
			Discovery: core.DiscoveryConfiguration{
				IncludeDomainRx: regexp.MustCompile(`foo`),
				ExcludeDomainRx: regexp.MustCompile(`2$`),
			},
		},
	}

	domains, err := listDomainsFiltered(cluster)
	if err != nil {
		t.Fatal("listDomainsFiltered failed with unexpected error:", err.Error())
	}
	names := make([]string, len(domains))
	for idx, d := range domains {
		names[idx] = d.Name
	}
	namesStr := strings.Join(names, " ")
	if namesStr != "foo1" {
		t.Errorf("expected only domain \"foo1\", but got \"%s\" instead", namesStr)
	}
}
