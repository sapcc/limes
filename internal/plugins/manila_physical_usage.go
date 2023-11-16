/*******************************************************************************
*
* Copyright 2023 SAP SE
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
	"fmt"
	"math"
	"time"

	"github.com/prometheus/common/model"
	"github.com/sapcc/go-bits/promquery"

	"github.com/sapcc/limes/internal/core"
)

// Collects various usage and physical usage metrics from Prometheus. This data
// is only known to the underlying NetApp filer, not to Manila itself, so we
// have to collect usage from NetApp metrics instead.
//
// The prober has a built-in cache since it is vastly more efficient to query
// Prometheus for all data at once every once in a while instead of querying
// for each individual project.
type manilaPhysicalUsageProber struct {
	client promquery.Client
	//cache
	filledAt *time.Time
	cache    map[manilaPhysicalUsageKey]*manilaPhysicalUsageData
}

type manilaPhysicalUsageKey struct {
	ProjectUUID   string
	ShareTypeName string
}

type manilaPhysicalUsageData struct {
	SharePhysicalUsage      uint64
	SnapshotPhysicalUsage   uint64
	SnapmirrorUsage         uint64
	SnapmirrorPhysicalUsage uint64
}

const (
	manilaSharePhysicalUsageQuery      = `sum by (project_id, share_type) (max by (project_id, share_id, share_type) (netapp_volume_used_bytes         {project_id!="",share_type!="",volume_type!="dp",volume_state="online"}))`
	manilaSnapshotPhysicalUsageQuery   = `sum by (project_id, share_type) (max by (project_id, share_id, share_type) (netapp_volume_snapshot_used_bytes{project_id!="",share_type!="",volume_type!="dp",volume_state="online"}))`
	manilaSnapmirrorUsageQuery         = `sum by (project_id, share_type) (max by (project_id, share_id, share_type) (netapp_volume_total_bytes        {project_id!="",share_type!="",volume_type!="dp",volume_state="online",snapshot_policy="EC2_Backups"}))`
	manilaSnapmirrorPhysicalUsageQuery = `sum by (project_id, share_type) (max by (project_id, share_id, share_type) (netapp_volume_used_bytes         {project_id!="",share_type!="",volume_type!="dp",volume_state="online",snapshot_policy="EC2_Backups"}))`
)

func newManilaPhysicalUsageProber(cfg *promquery.Config) (*manilaPhysicalUsageProber, error) {
	client, err := cfg.Connect()
	return &manilaPhysicalUsageProber{client: client}, err
}

func (p *manilaPhysicalUsageProber) fillCacheIfNecessary() error {
	//query Prometheus only on first call or if cache is too old
	if p.filledAt != nil && p.filledAt.After(time.Now().Add(-2*time.Minute)) {
		return nil
	}

	result := make(map[manilaPhysicalUsageKey]*manilaPhysicalUsageData)
	entryFor := func(sample *model.Sample) *manilaPhysicalUsageData {
		key := manilaPhysicalUsageKey{
			ProjectUUID:   string(sample.Metric["project_id"]),
			ShareTypeName: string(sample.Metric["share_type"]),
		}
		data := result[key]
		if result[key] == nil {
			data = &manilaPhysicalUsageData{}
			result[key] = data
		}
		return data
	}

	//collect physical usage for share_capacity
	vector, err := p.client.GetVector(manilaSharePhysicalUsageQuery)
	if err != nil {
		return fmt.Errorf("cannot collect share_capacity physical usage data: %w", err)
	}
	for _, sample := range vector {
		entryFor(sample).SharePhysicalUsage = roundUpIntoGigabytes(float64(sample.Value))
	}

	//collect physical usage for snapshot_capacity
	vector, err = p.client.GetVector(manilaSnapshotPhysicalUsageQuery)
	if err != nil {
		return fmt.Errorf("cannot collect snapshot_capacity physical usage data: %w", err)
	}
	for _, sample := range vector {
		entryFor(sample).SnapshotPhysicalUsage = roundUpIntoGigabytes(float64(sample.Value))
	}

	//collect usage for snapmirror_capacity
	vector, err = p.client.GetVector(manilaSnapmirrorUsageQuery)
	if err != nil {
		return fmt.Errorf("cannot collect snapmirror_capacity usage data: %w", err)
	}
	for _, sample := range vector {
		entryFor(sample).SnapmirrorUsage = roundUpIntoGigabytes(float64(sample.Value))
	}

	//collect physical usage for snapmirror_capacity
	vector, err = p.client.GetVector(manilaSnapmirrorPhysicalUsageQuery)
	if err != nil {
		return fmt.Errorf("cannot collect snapmirror_capacity physical usage data: %w", err)
	}
	for _, sample := range vector {
		entryFor(sample).SnapmirrorPhysicalUsage = roundUpIntoGigabytes(float64(sample.Value))
	}

	now := time.Now()
	p.filledAt = &now
	p.cache = result
	return nil
}

func roundUpIntoGigabytes(bytes float64) uint64 {
	return uint64(math.Ceil(bytes / (1 << 30)))
}

func (p *manilaPhysicalUsageProber) pick(project core.KeystoneProject, shareTypeName string) (manilaPhysicalUsageData, error) {
	err := p.fillCacheIfNecessary()
	if err != nil {
		return manilaPhysicalUsageData{}, err
	}
	entry := p.cache[manilaPhysicalUsageKey{
		ProjectUUID:   project.UUID,
		ShareTypeName: shareTypeName,
	}]
	if entry == nil {
		return manilaPhysicalUsageData{}, nil
	} else {
		return *entry, nil
	}
}

func (p *manilaPhysicalUsageProber) GetPhysicalUsages(project core.KeystoneProject, shareTypeName string) (sharePhysicalUsage, snapshotPhysicalUsage *uint64, err error) {
	entry, err := p.pick(project, shareTypeName)
	if err != nil {
		return nil, nil, err
	}
	return &entry.SharePhysicalUsage, &entry.SnapshotPhysicalUsage, nil
}

func (p *manilaPhysicalUsageProber) GetSnapmirrorUsage(project core.KeystoneProject, shareTypeName string) (core.ResourceData, error) {
	entry, err := p.pick(project, shareTypeName)
	if err != nil {
		return core.ResourceData{}, err
	}
	return core.ResourceData{
		Quota: 0, //NoQuota = true
		UsageData: core.InAnyAZ(core.UsageData{
			Usage:         entry.SnapmirrorUsage,
			PhysicalUsage: &entry.SnapmirrorPhysicalUsage,
		}),
	}, nil
}
