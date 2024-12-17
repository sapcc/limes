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
	"net/http"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/hypervisors"
	"github.com/gophercloud/gophercloud/v2/pagination"
)

// Like hypervisors.HypervisorPage, but fixes bug <https://github.com/gophercloud/gophercloud/issues/3222> by being a LinkedPageBase instead of a SinglePageBase.
type hypervisorPage struct {
	pagination.LinkedPageBase
}

// IsEmpty is required for LinkedPageBase to work.
func (page hypervisorPage) IsEmpty() (bool, error) {
	if page.StatusCode == http.StatusNoContent {
		return true, nil
	}

	var data struct {
		Hypervisors []struct{} `json:"hypervisors"`
	}
	err := page.ExtractInto(&data)
	return len(data.Hypervisors) == 0, err
}

// NextPageURL is required for LinkedPageBase to work.
func (page hypervisorPage) NextPageURL() (string, error) {
	var s struct {
		Links []gophercloud.Link `json:"hypervisors_links"`
	}
	err := page.ExtractInto(&s)
	if err != nil {
		return "", err
	}
	return gophercloud.ExtractNextURL(s.Links)
}

// Like hypervisors.List(), but fixes bug <https://github.com/gophercloud/gophercloud/issues/3222>.
func hypervisorsList(client *gophercloud.ServiceClient, opts hypervisors.ListOptsBuilder) pagination.Pager {
	url := client.ServiceURL("os-hypervisors", "detail")
	if opts != nil {
		query, err := opts.ToHypervisorListQuery()
		if err != nil {
			return pagination.Pager{Err: err}
		}
		url += query
	}

	return pagination.NewPager(client, url, func(r pagination.PageResult) pagination.Page {
		return hypervisorPage{pagination.LinkedPageBase{PageResult: r}}
	})
}
