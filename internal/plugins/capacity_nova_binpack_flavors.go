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

package plugins

import (
	"errors"
	"fmt"
	"slices"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/flavors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/plugins/nova"
)

type capacityNovaBinpackFlavorsPlugin struct {
	//configuration
	FlavorSelection     nova.FlavorSelection     `yaml:"flavor_selection"`
	HypervisorSelection nova.HypervisorSelection `yaml:"hypervisor_selection"`
	//connections
	NovaV2      *gophercloud.ServiceClient `yaml:"-"`
	PlacementV1 *gophercloud.ServiceClient `yaml:"-"`
}

func init() {
	core.CapacityPluginRegistry.Add(func() core.CapacityPlugin { return &capacityNovaBinpackFlavorsPlugin{} })
}

// Init implements the core.CapacityPlugin interface.
func (p *capacityNovaBinpackFlavorsPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	p.NovaV2, err = openstack.NewComputeV2(provider, eo)
	if err != nil {
		return err
	}
	p.PlacementV1, err = openstack.NewPlacementV1(provider, eo)
	if err != nil {
		return err
	}
	p.PlacementV1.Microversion = "1.6" //for traits endpoint

	return nil
}

// PluginTypeID implements the core.CapacityPlugin interface.
func (p *capacityNovaBinpackFlavorsPlugin) PluginTypeID() string {
	return "nova-binpack-flavors"
}

// Scrape implements the core.CapacityPlugin interface.
func (p *capacityNovaBinpackFlavorsPlugin) Scrape(backchannel core.CapacityPluginBackchannel) (result map[string]map[string]core.PerAZ[core.CapacityData], serializedMetrics []byte, err error) {
	//enumerate matching flavors, collect resource demand
	var (
		allFlavors                 []flavors.Flavor
		resourceDemandByFlavorName = make(map[string]map[limes.AvailabilityZone]core.ResourceDemand)
	)
	err = p.FlavorSelection.ForeachFlavor(p.NovaV2, func(f flavors.Flavor, extraSpecs map[string]string) error {
		allFlavors = append(allFlavors, f)
		resourceName := "instances_" + f.Name //TODO: use nova.FlavorTranslationTable?
		demand, err := backchannel.GetGlobalResourceDemand("compute", resourceName)
		if err != nil {
			return fmt.Errorf("while collecting resource demand for compute/%s: %w", resourceName, err)
		}
		resourceDemandByFlavorName[f.Name] = demand
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if len(allFlavors) == 0 {
		return nil, nil, errors.New("found no matching flavors")
	}

	//TODO: if available, use usage numbers reported by Nova to override ResourceDemand.Usage

	//enumerate matching hypervisors, prepare data structures for binpacking
	hypervisorsByAZ := make(map[limes.AvailabilityZone]nova.BinpackHypervisors)
	err = p.HypervisorSelection.ForeachHypervisor(p.NovaV2, p.PlacementV1, func(h nova.MatchingHypervisor) error {
		//TODO: report wellformed-ness of this HV via metrics (requires sharing the respective code with capacity_nova)

		//ignore HVs that are not associated with an aggregate and AZ
		if !h.CheckTopology() {
			return nil
		}

		bh, err := nova.PrepareHypervisorForBinpacking(h)
		if err != nil {
			return err
		}
		hypervisorsByAZ[h.AvailabilityZone] = append(hypervisorsByAZ[h.AvailabilityZone], bh)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	//during binpacking, place instances of large flavors first to achieve optimal results
	slices.SortFunc(allFlavors, func(lhs, rhs flavors.Flavor) int {
		//NOTE: this returns `rhs-lhs` instead of `lhs-rhs` to achieve descending order
		if lhs.VCPUs != rhs.VCPUs {
			return rhs.VCPUs - lhs.VCPUs
		}
		if lhs.RAM != rhs.RAM {
			return rhs.RAM - lhs.RAM
		}
		return rhs.Disk - lhs.Disk
	})

	//foreach AZ...
	for az, hypervisors := range hypervisorsByAZ {
		//place demanded instances in order of priority
		canPlaceFlavor := make(map[string]bool)
		for _, flavor := range allFlavors {
			canPlaceFlavor[flavor.Name] = true
		}
		for _, flavor := range allFlavors {
			if !hypervisors.PlaceSeveralInstances(flavor, "used", resourceDemandByFlavorName[flavor.Name][az].Usage) {
				canPlaceFlavor[flavor.Name] = false
			}
		}
		for _, flavor := range allFlavors {
			if !hypervisors.PlaceSeveralInstances(flavor, "committed", resourceDemandByFlavorName[flavor.Name][az].UnusedCommitments) {
				canPlaceFlavor[flavor.Name] = false
			}
		}
		for _, flavor := range allFlavors {
			if !hypervisors.PlaceSeveralInstances(flavor, "pending", resourceDemandByFlavorName[flavor.Name][az].PendingCommitments) {
				canPlaceFlavor[flavor.Name] = false
			}
		}

		//check how many instances we could place
		initiallyPlacedInstances := make(map[string]float64)
		totalPlacedInstances := make(map[string]float64) //these two will diverge in the next step
		for _, flavor := range allFlavors {
			count := hypervisors.PlacementCountForFlavor(flavor.Name)
			initiallyPlacedInstances[flavor.Name] = max(float64(count), 0.1)
			totalPlacedInstances[flavor.Name] = float64(count)
			//The max(..., 0.1) is explained below.
		}

		//fill up with padding in a fair way as long as there is space left
		//
		//This uses the Sainte-Laguë method designed for allocation of parliament
		//seats. In this case, the parties are the flavors, the votes are what we
		//allocated based on demand (`initiallyPlacedInstances`), and the seats are
		//the placements (`totalPlacedInstances`).
		for {
			var (
				bestFlavor *flavors.Flavor
				bestScore  = -1.0
			)
			for _, flavor := range allFlavors {
				if !canPlaceFlavor[flavor.Name] {
					continue
				}
				score := (initiallyPlacedInstances[flavor.Name]) / (2*totalPlacedInstances[flavor.Name] + 1)
				//^ This is why we adjusted all initiallyPlacedInstances[flavor.Name] = 0 to 0.1
				//above. If the nominator of this fraction is 0 for multiple flavors, the first
				//(biggest) flavor always wins unfairly. By adjusting to slightly away from zero,
				//the scoring is more fair and stable.
				if score > bestScore {
					bestScore = score
					flavor := flavor
					bestFlavor = &flavor
				}
			}
			if bestFlavor == nil {
				//no flavor left that can be placed -> stop
				break
			} else {
				if hypervisors.PlaceOneInstance(*bestFlavor, "padding") {
					totalPlacedInstances[bestFlavor.Name]++
				} else {
					canPlaceFlavor[bestFlavor.Name] = false
				}
			}
		}
	}

	//debug visualization of the binpack placement result
	if logg.ShowDebug {
		logg.Debug("binpackable flavors: %#v", allFlavors)
		logg.Debug("resource demand: %#v", resourceDemandByFlavorName)
		for az, hypervisors := range hypervisorsByAZ {
			for _, hypervisor := range hypervisors {
				hypervisor.RenderDebugView(az)
			}
		}
	}

	//build result
	computeResult := make(map[string]core.PerAZ[core.CapacityData])
	for _, flavor := range allFlavors {
		resourceResult := make(core.PerAZ[core.CapacityData], len(hypervisorsByAZ))
		for az, hypervisors := range hypervisorsByAZ {
			resourceResult[az] = &core.CapacityData{
				Capacity: hypervisors.PlacementCountForFlavor(flavor.Name),
				//TODO: if usage data from Nova is available, fill it in here
			}
		}
		computeResult["instances_"+flavor.Name] = resourceResult //TODO: use nova.FlavorTranslationTable?
	}
	//TODO: report hypervisors as subcapacities? (if so where? maybe just on the alphabetically first flavor?)

	return map[string]map[string]core.PerAZ[core.CapacityData]{"compute": computeResult}, nil, nil
}

// DescribeMetrics implements the core.CapacityPlugin interface.
func (p *capacityNovaBinpackFlavorsPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	//unused
}

// CollectMetrics implements the core.CapacityPlugin interface.
func (p *capacityNovaBinpackFlavorsPlugin) CollectMetrics(ch chan<- prometheus.Metric, serializedMetrics []byte) error {
	return nil //unused
}
