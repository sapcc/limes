// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package cinder

import "github.com/gophercloud/gophercloud/v2"

// TODO: Workaround until https://github.com/gophercloud/gophercloud/pull/3454 is merged and released.
// Remove the ListOpts type once the referenced PR is merged and released.

type ListOpts struct {
	// Specifies whether the query should include public or private Volume Types.
	// By default, it queries both types.
	IsPublic visibility `q:"is_public"`
	// Comma-separated list of sort keys and optional sort directions in the
	// form of <key>[:<direction>].
	Sort string `q:"sort"`
	// Requests a page size of items.
	Limit int `q:"limit"`
	// Used in conjunction with limit to return a slice of items.
	Offset int `q:"offset"`
	// The ID of the last-seen item.
	Marker string `q:"marker"`
}

type visibility string

const (
	// VisibilityDefault enables querying both public and private Volume Types.
	VisibilityDefault visibility = "None"
	// VisibilityPublic restricts the query to only public Volume Types.
	VisibilityPublic visibility = "true"
	// VisibilityPrivate restricts the query to only private Volume Types.
	VisibilityPrivate visibility = "false"
)

// ToVolumeTypeListQuery formats a ListOpts into a query string.
func (opts ListOpts) ToVolumeTypeListQuery() (string, error) {
	q, err := gophercloud.BuildQueryString(opts)
	return q.String(), err
}
