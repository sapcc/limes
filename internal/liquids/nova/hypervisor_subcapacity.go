// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package nova

import (
	"encoding/json"
	"fmt"

	"github.com/sapcc/go-api-declarations/liquid"
)

// pooledSubcapacityBuilder is used to build subcapacity lists for pooled resources.
type pooledSubcapacityBuilder struct {
	CoresSubcapacities     []liquid.Subcapacity
	InstancesSubcapacities []liquid.Subcapacity
	RAMSubcapacities       []liquid.Subcapacity
}

type subcapacityAttributes struct {
	AggregateName string   `json:"aggregate_name"`
	Traits        []string `json:"traits"`
}

func (b *pooledSubcapacityBuilder) addHypervisor(h matchingHypervisor, maxRootDiskSize float64) error {
	pc := h.partialCapacity()

	attrs := subcapacityAttributes{
		AggregateName: h.AggregateName,
		Traits:        h.Traits,
	}
	buf, err := json.Marshal(attrs)
	if err != nil {
		return fmt.Errorf("while serializing Subcapacity Attributes: %w", err)
	}

	hvCoresCapa := pc.intoCapacityData("cores", maxRootDiskSize, nil)
	b.CoresSubcapacities = append(b.CoresSubcapacities, liquid.Subcapacity{
		Name:       h.Hypervisor.Service.Host,
		Capacity:   hvCoresCapa.Capacity,
		Usage:      hvCoresCapa.Usage,
		Attributes: json.RawMessage(buf),
	})
	hvInstancesCapa := pc.intoCapacityData("instances", maxRootDiskSize, nil)
	b.InstancesSubcapacities = append(b.InstancesSubcapacities, liquid.Subcapacity{
		Name:       h.Hypervisor.Service.Host,
		Capacity:   hvInstancesCapa.Capacity,
		Usage:      hvInstancesCapa.Usage,
		Attributes: json.RawMessage(buf),
	})
	hvRAMCapa := pc.intoCapacityData("ram", maxRootDiskSize, nil)
	b.RAMSubcapacities = append(b.RAMSubcapacities, liquid.Subcapacity{
		Name:       h.Hypervisor.Service.Host,
		Capacity:   hvRAMCapa.Capacity,
		Usage:      hvRAMCapa.Usage,
		Attributes: json.RawMessage(buf),
	})

	return nil
}
