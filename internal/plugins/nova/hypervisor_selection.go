/*******************************************************************************
*
* Copyright 2017-2024 SAP SE
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
	"fmt"
	"slices"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/aggregates"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/hypervisors"
	"github.com/gophercloud/gophercloud/openstack/placement/v1/resourceproviders"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/regexpext"
)

// HypervisorSelection describes a set of hypervisors.
type HypervisorSelection struct {
	//Only match hypervisors with a hypervisor_type attribute matching this pattern.
	HypervisorTypeRx regexpext.PlainRegexp `yaml:"hypervisor_type_pattern"`
	//Only matchhypervisors that have any of these traits.
	RequiredTraits []string `yaml:"required_traits"`
	//Exclude hypervisors that have any of these traits.
	ExcludedTraits []string `yaml:"excluded_traits"`
	//Only match hypervisors that reside in an aggregate matching this pattern.
	//If a hypervisor resides in multiple matching aggregates, an error is raised.
	AggregateNameRx regexpext.PlainRegexp `yaml:"aggregate_name_pattern"`
}

// ForeachHypervisor lists all Nova hypervisors matching this
// HypervisorSelection, and calls the given callback once for each of them.
func (s HypervisorSelection) ForeachHypervisor(novaV2, placementV1 *gophercloud.ServiceClient, action func(MatchingHypervisor) error) error {
	//enumerate hypervisors
	page, err := hypervisors.List(novaV2, nil).AllPages()
	if err != nil {
		return fmt.Errorf("while listing hypervisors: %w", err)
	}
	var hypervisorData struct {
		Hypervisors []Hypervisor `json:"hypervisors"`
	}
	err = page.(hypervisors.HypervisorPage).ExtractInto(&hypervisorData)
	if err != nil {
		return fmt.Errorf("while listing hypervisors: %w", err)
	}

	//enumerate aggregates which establish the hypervisor <-> AZ mapping
	page, err = aggregates.List(novaV2).AllPages()
	if err != nil {
		return fmt.Errorf("while listing aggregates: %w", err)
	}
	allAggregates, err := aggregates.ExtractAggregates(page)
	if err != nil {
		return fmt.Errorf("while listing aggregates: %w", err)
	}

	//enumerate resource providers (there should be one resource provider per hypervisor)
	page, err = resourceproviders.List(placementV1, nil).AllPages()
	if err != nil {
		return fmt.Errorf("while listing resource providers: %w", err)
	}
	allResourceProviders, err := resourceproviders.ExtractResourceProviders(page)
	if err != nil {
		return fmt.Errorf("while listing resource providers: %w", err)
	}

	//foreach hypervisor...
OUTER:
	for _, h := range hypervisorData.Hypervisors {
		//check hypervisor type
		if !s.HypervisorTypeRx.MatchString(h.HypervisorType) {
			//NOTE: If no pattern was given, the regex will be empty and thus always match.
			logg.Debug("ignoring %s because hypervisor_type %q does not match", h.Description(), h.HypervisorType)
			continue
		}

		//check resource provider traits
		providerID, err := h.getResourceProviderID(allResourceProviders)
		if err != nil {
			return err
		}
		traits, err := resourceproviders.GetTraits(placementV1, providerID).Extract()
		if err != nil {
			return fmt.Errorf("while getting traits for resource provider %s: %w", providerID, err)
		}
		for _, trait := range s.RequiredTraits {
			if !slices.Contains(traits.Traits, trait) {
				logg.Debug("ignoring %s because trait %q is missing", h.Description(), trait)
				continue OUTER
			}
		}
		for _, trait := range s.ExcludedTraits {
			if slices.Contains(traits.Traits, trait) {
				logg.Debug("ignoring %s because trait %q is present", h.Description(), trait)
				continue OUTER
			}
		}

		//check that resource provider reports any capacity (we want to ignore
		//half-configured hypervisors that are still in buildup)
		inventories, err := resourceproviders.GetInventories(placementV1, providerID).Extract()
		if err != nil {
			return fmt.Errorf("while getting inventories for resource provider %s: %w", providerID, err)
		}
		usages, err := resourceproviders.GetUsages(placementV1, providerID).Extract()
		if err != nil {
			return fmt.Errorf("while getting usages for resource provider %s: %w", providerID, err)
		}
		for _, metric := range []string{"VCPU", "MEMORY_MB", "DISK_GB"} {
			if inventories.Inventories[metric].Total == 0 {
				logg.Debug("ignoring %s because Placement reports zero %s capacity", h.Description(), metric)
				continue OUTER
			}
		}

		//match hypervisor with AZ and relevant aggregate
		matchingAZs := make(map[limes.AvailabilityZone]bool)
		matchingAggregates := make(map[string]bool)
		for _, aggr := range allAggregates {
			if !h.isInAggregate(aggr) {
				continue
			}
			if s.AggregateNameRx.MatchString(aggr.Name) {
				matchingAggregates[aggr.Name] = true
			}
			if az := limes.AvailabilityZone(aggr.AvailabilityZone); az != "" {
				//We also use aggregates not matching our naming pattern to establish a
				//hypervisor <-> AZ relationship. We have observed in the wild that
				//matching aggregates do not always have their AZ field maintained.
				matchingAZs[az] = true
			}
		}

		//the mapping from hypervisor to aggregate/AZ must be unique (otherwise the
		//capacity will be counted either not at all or multiple times)
		//
		//NOTE: We leave it to the caller to discard HVs without aggregate or AZ.
		//This is a state that can happen during buildup, and we want to see it in metrics.
		if len(matchingAggregates) > 1 {
			return fmt.Errorf("%s could not be uniquely matched to an aggregate (matching aggregates = %v)", h.Description(), matchingAggregates)
		}
		if len(matchingAZs) > 1 {
			return fmt.Errorf("%s could not be uniquely matched to an AZ (matching AZs = %v)", h.Description(), matchingAZs)
		}
		var (
			matchingAggregateName string
			matchingAZ            limes.AvailabilityZone
		)
		for aggr := range matchingAggregates {
			matchingAggregateName = aggr
		}
		for az := range matchingAZs {
			matchingAZ = az
		}

		err = action(MatchingHypervisor{
			Hypervisor:       h,
			AggregateName:    matchingAggregateName,
			AvailabilityZone: matchingAZ,
			Traits:           traits.Traits,
			Inventories:      inventories.Inventories,
			Usages:           usages.Usages,
		})
		if err != nil {
			return err
		}
	}

	return nil
}

// MatchingHypervisor is the callback argmuent for
// func HypervisorSelection.ForeachHypervisor().
type MatchingHypervisor struct {
	//information from Nova
	Hypervisor       Hypervisor
	AggregateName    string
	AvailabilityZone limes.AvailabilityZone
	//information from Placement
	Traits      []string
	Inventories map[string]resourceproviders.Inventory
	Usages      map[string]int
}

// CheckTopology logs an error and returns false if the hypervisor is not
// associated with an aggregate and AZ.
//
// This is not a fatal error: During buildup, new hypervisors may not be mapped
// to an aggregate to prevent scheduling of instances onto them - we just log
// an error and ignore this hypervisor's capacity.
func (h MatchingHypervisor) CheckTopology() bool {
	if h.AggregateName == "" {
		logg.Error("%s does not belong to any matching aggregates", h.Hypervisor.Description())
		return false
	}
	if h.AvailabilityZone == "" {
		logg.Error("%s could not be matched to any AZ (aggregate = %q)", h.Hypervisor.Description(), h.AggregateName)
		return false
	}
	return true
}

// PartialCapacity formats this hypervisor's capacity.
func (h MatchingHypervisor) PartialCapacity() PartialCapacity {
	makeMetric := func(metric string) PartialCapacityMetric {
		return PartialCapacityMetric{
			Capacity: uint64(h.Inventories[metric].Total - h.Inventories[metric].Reserved),
			Usage:    uint64(h.Usages[metric]),
		}
	}
	return PartialCapacity{
		VCPUs:              makeMetric("VCPU"),
		MemoryMB:           makeMetric("MEMORY_MB"),
		LocalGB:            makeMetric("DISK_GB"),
		RunningVMs:         h.Hypervisor.RunningVMs,
		MatchingAggregates: map[string]bool{h.AggregateName: true},
	}
}

// Hypervisor represents a Nova hypervisor as returned by the Nova API.
//
// We are not using the hypervisors.Hypervisor type provided by Gophercloud.
// In our clusters, that type breaks because some hypervisors report unexpected
// NULL values on fields that we are not even interested in.
type Hypervisor struct {
	ID                 int    `json:"id"`
	HypervisorHostname string `json:"hypervisor_hostname"`
	HypervisorType     string `json:"hypervisor_type"`
	// LocalGB            uint64              `json:"local_gb"`
	// MemoryMB           uint64              `json:"memory_mb"`
	// MemoryMBUsed       uint64              `json:"memory_mb_used"`
	RunningVMs uint64              `json:"running_vms"`
	Service    hypervisors.Service `json:"service"`
	// VCPUs              uint64              `json:"vcpus"`
	// VCPUsUsed          uint64              `json:"vcpus_used"`
}

// Description returns a string that identifies this hypervisor in log messages.
func (h Hypervisor) Description() string {
	return fmt.Sprintf("Nova hypervisor %s with .service.host %q", h.HypervisorHostname, h.Service.Host)
}

func (h Hypervisor) isInAggregate(aggr aggregates.Aggregate) bool {
	for _, host := range aggr.Hosts {
		if h.Service.Host == host {
			return true
		}
	}
	return false
}

func (h Hypervisor) getResourceProviderID(resourceProviders []resourceproviders.ResourceProvider) (string, error) {
	for _, rp := range resourceProviders {
		if rp.Name == h.HypervisorHostname {
			return rp.UUID, nil
		}
	}
	return "", fmt.Errorf("cannot find resource provider for hypervisor_hostname = %q", h.HypervisorHostname)
}
