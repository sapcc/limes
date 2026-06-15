// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package reports_v2

import (
	"github.com/sapcc/limes/internal/apideclarations/apiv2"
	"github.com/sapcc/limes/internal/core"
)

// ResourceFilter is a struct which can be constructed from optsv2.ResourceOpts.
// It applies the requested filters to the available resources.
type ResourceFilter struct {
	ServicesAreFiltered  bool
	ResourcesAreFiltered bool
	FilteredServices     core.ServicesByType
	FilteredResources    core.ResourcesByNameType
	FilteredAZResources  core.AZResourcesByAZNameType
}

// MakeResourceFilter constructs a ResourceFilter from the given optsv2.ResourceOpts
// and the cluster's services and resources.
func MakeResourceFilter(opts apiv2.ResourceReportOpts, cluster *core.Cluster) (rf ResourceFilter, err error) {
	return rf, nil
}
