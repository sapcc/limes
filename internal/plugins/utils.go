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
	"reflect"

	"github.com/sapcc/go-api-declarations/liquid"
)

func p2u64(val uint64) *uint64 {
	return &val
}

func checkResourceTopologies(serviceInfo liquid.ServiceInfo) (err error) {
	invalidTopologies := map[liquid.ResourceName]liquid.ResourceTopology{}
	resources := serviceInfo.Resources
	for k, v := range resources {
		if !v.Topology.IsValid() || v.Topology != "" {
			invalidTopologies[k] = v.Topology
		}
	}
	if len(invalidTopologies) > 0 {
		return fmt.Errorf("invalid topologies detected: %v", invalidTopologies)
	}
	return
}

func matchLiquidReportToTopology[V any](perAZReport map[liquid.AvailabilityZone]*V, topology liquid.ResourceTopology) (err error) {
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
	}
	return fmt.Errorf("scrape with toplogy type: %s returned AZs: %v", topology, reflect.ValueOf(perAZReport).MapKeys())
}
