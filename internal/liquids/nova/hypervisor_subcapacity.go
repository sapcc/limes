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
