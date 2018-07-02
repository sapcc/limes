/*******************************************************************************
*
* Copyright 2018 SAP SE
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
	"encoding/json"
	"strconv"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/pagination"
)

type ironicClient struct {
	*gophercloud.ServiceClient
}

func newIronicClient(provider *gophercloud.ProviderClient) (*ironicClient, error) {
	serviceType := "baremetal"
	eo := gophercloud.EndpointOpts{Availability: gophercloud.AvailabilityPublic}
	eo.ApplyDefaults(serviceType)

	url, err := provider.EndpointLocator(eo)
	if err != nil {
		return nil, err
	}
	return &ironicClient{
		ServiceClient: &gophercloud.ServiceClient{
			ProviderClient: provider,
			Endpoint:       url,
			Type:           serviceType,
		},
	}, nil
}

////////////////////////////////////////////////////////////////////////////////
// list nodes

type ironicNode struct {
	ID                   string  `json:"uuid"`
	Name                 string  `json:"name"`
	ProvisionState       string  `json:"provision_state"`
	TargetProvisionState *string `json:"target_provision_state"`
	Properties           struct {
		Cores           veryFlexibleUint64 `json:"cpus"`
		DiskGiB         veryFlexibleUint64 `json:"local_gb"`
		MemoryMiB       veryFlexibleUint64 `json:"memory_mb"`
		CPUArchitecture string             `json:"cpu_arch"`
		Capabilities    string             `json:"capabilities"` //e.g. "cpu_txt:true,cpu_aes:true"
		SerialNumber    string             `json:"serial"`
	} `json:"properties"`
}

func (n ironicNode) StableProvisionState() string {
	if n.TargetProvisionState != nil {
		return *n.TargetProvisionState
	}
	return n.ProvisionState
}

func extractNodes(page pagination.Page) (nodes []ironicNode, err error) {
	err = page.(ironicNodePage).Result.ExtractIntoSlicePtr(&nodes, "nodes")
	return
}

type ironicNodePage struct {
	pagination.MarkerPageBase
}

func (p ironicNodePage) IsEmpty() (bool, error) {
	nodes, err := extractNodes(p)
	return len(nodes) == 0, err
}

func (p ironicNodePage) LastMarker() (string, error) {
	nodes, err := extractNodes(p)
	if err != nil || len(nodes) == 0 {
		return "", err
	}
	return nodes[len(nodes)-1].ID, nil
}

func (c ironicClient) GetNodes() ([]ironicNode, error) {
	url := c.ServiceURL("nodes", "detail")
	pager := pagination.NewPager(c.ServiceClient, url, func(r pagination.PageResult) pagination.Page {
		page := ironicNodePage{pagination.MarkerPageBase{PageResult: r}}
		page.MarkerPageBase.Owner = page
		return page
	})
	//if this is not set, the provision_state fields will be there,
	//but always be null ... #justopenstackthings
	pager.Headers = map[string]string{
		"X-Openstack-Ironic-Api-Version": "1.22",
	}

	var result []ironicNode
	err := pager.EachPage(func(page pagination.Page) (bool, error) {
		pageNodes, err := extractNodes(page)
		if err != nil {
			return false, err
		}
		result = append(result, pageNodes...)
		return true, nil
	})
	return result, err
}

////////////////////////////////////////////////////////////////////////////////
// OpenStack is being inconsistent with itself again

//For fields that are sometimes missing, sometimes an integer, sometimes a string.
type veryFlexibleUint64 uint64

//UnmarshalJSON implements the json.Unmarshaler interface.
func (value *veryFlexibleUint64) UnmarshalJSON(buf []byte) error {
	if string(buf) == "null" {
		*value = 0
		return nil
	}

	if buf[0] == '"' {
		var str string
		err := json.Unmarshal(buf, &str)
		if err != nil {
			return err
		}
		val, err := strconv.ParseUint(str, 10, 64)
		*value = veryFlexibleUint64(val)
		return err
	}

	var val uint64
	err := json.Unmarshal(buf, &val)
	*value = veryFlexibleUint64(val)
	return err
}
