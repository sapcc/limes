/*******************************************************************************
*
* Copyright 2017 SAP SE
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
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/objectstorage/v1/accounts"
	"github.com/sapcc/limes/pkg/limes"
	"regexp"
)

type swiftPlugin struct{}

var swiftResources = []limes.ResourceInfo{
	limes.ResourceInfo{
		Name: "capacity",
		Unit: limes.UnitBytes,
	},
}

//TODO Make Auth prefix configurable
var urlRegex = regexp.MustCompile("v1/AUTH_([a-zA-Z0-9]+)")

func init() {
	limes.RegisterPlugin(&swiftPlugin{})
}

//ServiceType implements the limes.Plugin interface.
func (p *swiftPlugin) ServiceType() string {
	return "object-store"
}

//Resources implements the limes.Plugin interface.
func (p *swiftPlugin) Resources() []limes.ResourceInfo {
	return swiftResources
}

func (p *swiftPlugin) Client(driver limes.Driver) (*gophercloud.ServiceClient, error) {
	return openstack.NewObjectStorageV1(driver.Client(),
		gophercloud.EndpointOpts{Availability: gophercloud.AvailabilityPublic},
	)
}

//Scrape implements the limes.Plugin interface.
func (p *swiftPlugin) Scrape(driver limes.Driver, domainUUID, projectUUID string) (map[string]limes.ResourceData, error) {
	client, err := p.ProjectClient(driver, projectUUID)
	if err != nil {
		return nil, err
	}

	result := accounts.Get(client, accounts.GetOpts{})
	metaData, err := result.ExtractMetadata()
	if err != nil {
		return nil, err
	}

	var quota int64 = -1
	quota_header, ok := metaData["X-Account-Meta-Quota-Bytes"]
	if ok {
		quota = int64(quota_header)
	}

	return map[string]limes.ResourceData{
		"capacity": limes.ResourceData{
			Quota: quota,
			Usage: uint64(metaData["X-Account-Bytes-Used"]),
		},
	}, nil
}

//SetQuota implements the limes.Plugin interface.
func (p *swiftPlugin) SetQuota(driver limes.Driver, domainUUID, projectUUID string, quotas map[string]uint64) error {
	client, err := p.ProjectClient(driver, projectUUID)
	if err != nil {
		return err
	}

	return accounts.Update(client, accounts.UpdateOpts{
		Metadata: map[string]string{"X-Account-Meta-Quota-Bytes": string(quotas["capacity"])},
	}).Err
}

//Capacity implements the limes.Plugin interface.
func (p *swiftPlugin) Capacity(driver limes.Driver) (map[string]uint64, error) {
	//TODO implement
	return map[string]uint64{}, nil
}

//Get the project specif storage URL scoped client
func (p *swiftPlugin) ProjectClient(driver limes.Driver, projectUUID string) (*gophercloud.ServiceClient, error) {
	client, err := p.Client(driver)
	if err != nil {
		return nil, err
	}

	//We act as Reseller_Admin here, but cannot use the endpoint url returned from catalogue
	//Replace the resellers project id with the requested one
	client.Endpoint = urlRegex.ReplaceAllString(client.Endpoint, projectUUID)
	return client, nil
}