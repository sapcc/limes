// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package nova

import (
	"encoding/json"
	"fmt"

	"github.com/sapcc/go-api-declarations/liquid"
)

// PooledSubcapacityBuilder is used to build subcapacity lists for pooled resources.
type PooledSubcapacityBuilder struct {
	CoresSubcapacities     []liquid.Subcapacity
	InstancesSubcapacities []liquid.Subcapacity
	RAMSubcapacities       []liquid.Subcapacity
}

type SubcapacityAttributes struct {
	AggregateName string   `json:"aggregate_name"`
	Traits        []string `json:"traits"`
}

func (b *PooledSubcapacityBuilder) AddHypervisor(h MatchingHypervisor, maxRootDiskSize float64) error {
	pc := h.PartialCapacity()

	attrs := SubcapacityAttributes{
		AggregateName: h.AggregateName,
		Traits:        h.Traits,
	}
	buf, err := json.Marshal(attrs)
	if err != nil {
		return fmt.Errorf("while serializing Subcapacity Attributes: %w", err)
	}

	hvCoresCapa := pc.IntoCapacityData("cores", maxRootDiskSize, nil)
	b.CoresSubcapacities = append(b.CoresSubcapacities, liquid.Subcapacity{
		Name:       h.Hypervisor.Service.Host,
		Capacity:   hvCoresCapa.Capacity,
		Usage:      hvCoresCapa.Usage,
		Attributes: json.RawMessage(buf),
	})
	hvInstancesCapa := pc.IntoCapacityData("instances", maxRootDiskSize, nil)
	b.InstancesSubcapacities = append(b.InstancesSubcapacities, liquid.Subcapacity{
		Name:       h.Hypervisor.Service.Host,
		Capacity:   hvInstancesCapa.Capacity,
		Usage:      hvInstancesCapa.Usage,
		Attributes: json.RawMessage(buf),
	})
	hvRAMCapa := pc.IntoCapacityData("ram", maxRootDiskSize, nil)
	b.RAMSubcapacities = append(b.RAMSubcapacities, liquid.Subcapacity{
		Name:       h.Hypervisor.Service.Host,
		Capacity:   hvRAMCapa.Capacity,
		Usage:      hvRAMCapa.Usage,
		Attributes: json.RawMessage(buf),
	})

	return nil
}
