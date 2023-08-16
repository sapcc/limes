/*******************************************************************************
*
* Copyright 2022 SAP SE
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
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack/baremetal/v1/nodes"
	"github.com/gophercloud/gophercloud/pagination"
)

// Like `nodes.ListDetail(client, nil)`, but works around <https://github.com/gophercloud/gophercloud/issues/2431>.
func ironicNodesListDetail(client *gophercloud.ServiceClient) pagination.Pager {
	url := client.ServiceURL("nodes", "detail")
	return pagination.NewPager(client, url, func(r pagination.PageResult) pagination.Page {
		return ironicNodePage{nodes.NodePage{LinkedPageBase: pagination.LinkedPageBase{PageResult: r}}}
	})
}

// Like `nodes.ExtractNodesInto()`, but casts into the correct pagination.Page type.
func ironicExtractNodesInto(r pagination.Page, v any) error {
	return r.(ironicNodePage).Result.ExtractIntoSlicePtr(v, "nodes")
}

type ironicNodePage struct {
	nodes.NodePage
}

// NextPageURL uses the response's embedded link reference to navigate to the
// next page of results.
func (r ironicNodePage) NextPageURL() (string, error) {
	var s struct {
		Next string `json:"next"`
	}
	err := r.ExtractInto(&s)
	if err != nil {
		return "", err
	}
	if s.Next != "" {
		return s.Next, nil
	}
	//fallback
	return r.NodePage.NextPageURL()
}
