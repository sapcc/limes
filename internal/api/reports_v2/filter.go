// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package reports_v2

import (
	"github.com/sapcc/go-api-declarations/liquid"

	"github.com/sapcc/limes/internal/db"
)

// ResourceFilter is a struct which can be constructed from optsv2.ResourceOpts.
// It applies the requested filters to the available resources.
type ResourceFilter struct {
	ServicesAreFiltered  bool
	ResourcesAreFiltered bool
	FilteredServices     map[db.ServiceType]db.Service
	FilteredResources    map[db.ServiceType]map[liquid.ResourceName]db.Resource
}

/*
// MakeResourceFilter constructs a ResourceFilter from the given optsv2.ResourceOpts
// and the cluster's services and resources.
func MakeResourceFilter(opts optsv2.ResourceOpts, cluster *core.Cluster) (rf ResourceFilter, err error ) {

}

ScrapedAtFilter struct {

}*/
