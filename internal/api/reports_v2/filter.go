// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package reports_v2

import (
	"fmt"
	"regexp"
	"strconv"

	"github.com/lib/pq"

	"github.com/sapcc/limes/internal/apideclarations/apiv2/common"
	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"
)

// Filter is a version of FilteredServiceInfoSnapshot which gets
// constructed from API query options. It has a method for applying
// the FilteredServiceInfoSnapshot values to sql strings, to allow
// for less joins of service info related tables.
type Filter struct {
	core.FilteredServiceInfoSnapshot
}

// FilterFromResourceOpts returns a Filter from apiv2.ResourceReportOpts.
func FilterFromResourceOpts(cluster *core.Cluster, opts common.ResourceReportOpts) (f Filter, err error) {
	sis := cluster.SIC.GetSnapshot()
	f = Filter{sis.Filter(core.ServiceInfoFilter{
		ServiceArea:  opts.Area,
		ServiceType:  opts.ServiceType,
		Category:     opts.Category,
		ResourceName: opts.ResourceName,
	})}
	services := f.GetServices()
	if area, ok := opts.Area.Unpack(); ok && len(services) == 0 {
		return f, fmt.Errorf(`no services found for area %q`, area)
	}
	if serviceType, ok := opts.ServiceType.Unpack(); ok && len(services) == 0 {
		return f, fmt.Errorf(`no services found for type %q`, serviceType)
	}
	resources := f.GetResources()
	if category, ok := opts.Category.Unpack(); ok && len(resources) == 0 {
		return f, fmt.Errorf(`no resources found for category %q`, category)
	}
	if name, ok := opts.ResourceName.Unpack(); ok && len(resources) == 0 {
		return f, fmt.Errorf(`no resources found for name %q`, name)
	}
	return f, nil
}

// FilterFromRateOpts returns a Filter from apiv2.RateReportOpts.
func FilterFromRateOpts(cluster *core.Cluster, opts common.RateReportOpts) (f Filter, err error) {
	sis := cluster.SIC.GetSnapshot()
	f = Filter{sis.Filter(core.ServiceInfoFilter{
		ServiceArea: opts.Area,
		ServiceType: opts.ServiceType,
		Category:    opts.Category,
		RateName:    opts.RateName,
	})}
	services := f.GetServices()
	if area, ok := opts.Area.Unpack(); ok && len(services) == 0 {
		return f, fmt.Errorf(`no services found for area %q`, area)
	}
	if serviceType, ok := opts.ServiceType.Unpack(); ok && len(services) == 0 {
		return f, fmt.Errorf(`no services found for type %q`, serviceType)
	}
	rates := f.GetRates()
	if category, ok := opts.Category.Unpack(); ok && len(rates) == 0 {
		return f, fmt.Errorf(`no rates found for category %q`, category)
	}
	if name, ok := opts.RateName.Unpack(); ok && len(rates) == 0 {
		return f, fmt.Errorf(`no rates found for name %q`, name)
	}
	return f, nil
}

var filterReplaceRx = regexp.MustCompile(`{{(.*?) = ANY\(\$(service_id|resource_id|rate_id)\)}}`)

// ExpandServiceFilters takes an SQL query string with curly-bracketed
// where-clauses and will replace each one with an arg position and return the
// according SQL args for this filter, namely a list of entity IDs.
// The expressions must be of the form "{{[filter-field] = ANY($[id-field])}}"
// where filter-field can be a primary key column or a foreign key and id-field
// is the name of the entity whose ID-column values are used.
// It supports service_id, resource_id and rate_id.
// On unknown keywords it will panic.
func (f Filter) ExpandServiceFilters(originalQuery string, originalArgs ...any) (query string, args []any) {
	// get current highest index
	var err error
	i := 0
	queryVariables := regexp.MustCompile(`\$(\d+)`)
	matches := queryVariables.FindAllString(originalQuery, -1)
	if len(matches) > 0 {
		last := matches[len(matches)-1]
		i, err = strconv.Atoi(queryVariables.FindStringSubmatch(last)[1])
		if err != nil {
			panic("digits should be parseable integer")
		}
	}
	args = append(args, originalArgs...)

	query = filterReplaceRx.ReplaceAllStringFunc(originalQuery, func(matchStr string) string {
		// optimization: when not filtered, replace filter with no-op
		if f.FilterIsEmpty() {
			return util.SQLFilterNoop
		}
		match := filterReplaceRx.FindStringSubmatch(matchStr)

		switch match[2] {
		case "service_id":
			args = append(args, pq.Array(f.getServiceIDs()))
		case "resource_id":
			args = append(args, pq.Array(f.getResourceIDs()))
		case "rate_id":
			args = append(args, pq.Array(f.getRateIDs()))
		default:
			panic("unreachable")
		}
		i++
		return match[1] + " = ANY($" + strconv.Itoa(i) + ")"
	})
	return query, args
}

func (f Filter) getServiceIDs() (ids []db.ServiceID) {
	for _, service := range f.GetServices() {
		ids = append(ids, service.ID)
	}
	return ids
}

func (f Filter) getResourceIDs() (ids []db.ResourceID) {
	for _, resources := range f.GetResources() {
		for _, resource := range resources {
			ids = append(ids, resource.ID)
		}
	}
	return ids
}

func (f Filter) getRateIDs() (ids []db.RateID) {
	for _, rates := range f.GetRates() {
		for _, rate := range rates {
			ids = append(ids, rate.ID)
		}
	}
	return ids
}
