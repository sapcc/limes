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
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/sapcc/go-api-declarations/liquid"
)

func p2u64(val uint64) *uint64 {
	return &val
}

func SortedMapKeys[M map[K]V, K ~string, V any](mapToSort M) []K {
	sortedKeys := slices.Collect(maps.Keys(mapToSort))
	slices.Sort(sortedKeys)
	return sortedKeys
}

func CheckResourceTopologies(serviceInfo liquid.ServiceInfo) (err error) {
	// TODO: remove this special case once liquid-ceph has rolled out their topology support
	//       (if you come across this, it's probably time; ping Stefan M. to ask for confirmation)
	for resName, resInfo := range serviceInfo.Resources {
		if resName == "objectstore_region_3_hdd_capacity" && resInfo.Topology == "" {
			resInfo.Topology = liquid.FlatResourceTopology
			serviceInfo.Resources[resName] = resInfo
		}
	}

	var errs []error
	resources := serviceInfo.Resources

	resourceNames := SortedMapKeys(resources)
	for _, resourceName := range resourceNames {
		topology := resources[resourceName].Topology
		if !topology.IsValid() {
			errs = append(errs, fmt.Errorf("invalid topology: %s on resource: %s", topology, resourceName))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return
}

func MatchLiquidReportToTopology[V any](perAZReport map[liquid.AvailabilityZone]V, topology liquid.ResourceTopology) (err error) {
	_, anyExists := perAZReport[liquid.AvailabilityZoneAny]
	_, unknownExists := perAZReport[liquid.AvailabilityZoneUnknown]
	switch topology {
	case liquid.FlatResourceTopology:
		if len(perAZReport) == 1 && anyExists {
			return
		}
	case liquid.AZAwareResourceTopology:
		if len(perAZReport) > 0 && !anyExists {
			return
		}
	case liquid.AZSeparatedResourceTopology:
		if len(perAZReport) > 0 && !anyExists && !unknownExists {
			return
		}
	case "":
		return
	}

	reportedAZs := SortedMapKeys(perAZReport)
	return fmt.Errorf("scrape with topology type: %s returned AZs: %v", topology, reportedAZs)
}
