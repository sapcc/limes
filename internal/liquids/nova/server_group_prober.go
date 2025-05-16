// SPDX-FileCopyrightText: 2020 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package nova

import (
	"context"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servergroups"
)

// ServerGroupProber measures server_group_members usage within a project.
//
// The reason why this type exists at all is that:
// a) Nova does not report the usage for server_group_members directly, and
// b) we cannot ask for server groups in a specific foreign project.
//
// We can only list *all* server groups globally at once. Since this is very
// expensive, we only do it once every few minutes.
type ServerGroupProber struct {
	novaV2           *gophercloud.ServiceClient
	usageByProjectID map[string]uint64
	lastScrapeTime   time.Time // only initialized if .usageByProjectID != nil
}

// NewServerGroupProber builds a ServerGroupProber instance.
func NewServerGroupProber(novaV2 *gophercloud.ServiceClient) *ServerGroupProber {
	return &ServerGroupProber{novaV2: novaV2}
}

// GetMemberUsageForProject returns server_group_members usage in the given project.
func (p *ServerGroupProber) GetMemberUsageForProject(ctx context.Context, projectID string) (uint64, error) {
	// refresh cache if not initialized or outdated
	var err error
	if p.usageByProjectID == nil || time.Since(p.lastScrapeTime) > 10*time.Minute {
		err = p.fillCache(ctx)
	}

	return p.usageByProjectID[projectID], err
}

func (p *ServerGroupProber) fillCache(ctx context.Context) error {
	// When paginating through the list of server groups, perform steps slightly
	// smaller than the actual page size, in order to correctly detect insertions
	// and deletions that may cause list entries to shift around while we iterate
	// over them.
	const pageSize int = 500
	stepSize := pageSize * 9 / 10
	var currentOffset int
	serverGroupSeen := make(map[string]bool)
	usageByProjectID := make(map[string]uint64)
	for {
		groups, err := p.getServerGroupsPage(ctx, pageSize, currentOffset)
		if err != nil {
			return err
		}
		for _, sg := range groups {
			if !serverGroupSeen[sg.ID] {
				usageByProjectID[sg.ProjectID] += uint64(len(sg.Members))
				serverGroupSeen[sg.ID] = true
			}
		}

		// abort after the last page
		if len(groups) < pageSize {
			break
		}
		currentOffset += stepSize
	}

	p.usageByProjectID = usageByProjectID
	p.lastScrapeTime = time.Now()
	return nil
}

func (p *ServerGroupProber) getServerGroupsPage(ctx context.Context, limit, offset int) ([]servergroups.ServerGroup, error) {
	allPages, err := servergroups.List(p.novaV2, servergroups.ListOpts{AllProjects: true, Limit: limit, Offset: offset}).AllPages(ctx)
	if err != nil {
		return nil, err
	}
	allServerGroups, err := servergroups.ExtractServerGroups(allPages)
	if err != nil {
		return nil, err
	}
	return allServerGroups, nil
}
