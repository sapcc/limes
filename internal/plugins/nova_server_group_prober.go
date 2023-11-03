/*******************************************************************************
*
* Copyright 2020-2023 SAP SE
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

package plugins

import (
	"time"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/servergroups"
)

// The reason why this type exists at all is that:
// a) Nova does not report the usage for server_group_members directly, and
// b) we cannot ask for server groups in a specific foreign project.
//
// We can only list *all* server groups globally at once. Since this is very
// expensive, we only do it once every few minutes.
type novaServerGroupProber struct {
	NovaV2           *gophercloud.ServiceClient
	UsageByProjectID map[string]uint64
	LastScrapeTime   time.Time // only initialized if .usageByProjectID != nil
}

func newNovaServerGroupProber(novaV2 *gophercloud.ServiceClient) *novaServerGroupProber {
	return &novaServerGroupProber{NovaV2: novaV2}
}

func (p *novaServerGroupProber) GetMemberUsageForProject(projectID string) (uint64, error) {
	//refresh cache if not initialized or outdated
	var err error
	if p.UsageByProjectID == nil || time.Since(p.LastScrapeTime) > 10*time.Minute {
		err = p.fillCache()
	}

	return p.UsageByProjectID[projectID], err
}

func (p *novaServerGroupProber) fillCache() error {
	//When paginating through the list of server groups, perform steps slightly
	//smaller than the actual page size, in order to correctly detect insertions
	//and deletions that may cause list entries to shift around while we iterate
	//over them.
	const pageSize int = 500
	stepSize := pageSize * 9 / 10
	var currentOffset int
	serverGroupSeen := make(map[string]bool)
	usageByProjectID := make(map[string]uint64)
	for {
		groups, err := p.getServerGroupsPage(pageSize, currentOffset)
		if err != nil {
			return err
		}
		for _, sg := range groups {
			if !serverGroupSeen[sg.ID] {
				usageByProjectID[sg.ProjectID] += uint64(len(sg.Members))
				serverGroupSeen[sg.ID] = true
			}
		}

		//abort after the last page
		if len(groups) < pageSize {
			break
		}
		currentOffset += stepSize
	}

	p.UsageByProjectID = usageByProjectID
	p.LastScrapeTime = time.Now()
	return nil
}

func (p *novaServerGroupProber) getServerGroupsPage(limit, offset int) ([]servergroups.ServerGroup, error) {
	allPages, err := servergroups.List(p.NovaV2, servergroups.ListOpts{AllProjects: true, Limit: limit, Offset: offset}).AllPages()
	if err != nil {
		return nil, err
	}
	allServerGroups, err := servergroups.ExtractServerGroups(allPages)
	if err != nil {
		return nil, err
	}
	return allServerGroups, nil
}
