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
	"net/http"
	"regexp"
	"strconv"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/objectstorage/v1/accounts"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/util"
)

type swiftPlugin struct{}

var swiftResources = []limes.ResourceInfo{
	limes.ResourceInfo{
		Name: "capacity",
		Unit: limes.UnitBytes,
	},
}

//TODO Make Auth prefix configurable
var urlRegex = regexp.MustCompile("(v1/AUTH_)[a-zA-Z0-9]+")

func init() {
	limes.RegisterQuotaPlugin(&swiftPlugin{})
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
	client, err := p.projectClient(driver, projectUUID)
	if err != nil {
		return nil, err
	}

	//Get account metadata
	account := getAccount(client, projectUUID)
	if account == nil {
		//Swift account does not exist, but the keystone project
		return map[string]limes.ResourceData{
			"capacity": limes.ResourceData{
				Quota: 0,
				Usage: 0,
			},
		}, nil
	} else if account.Err != nil {
		return nil, err
	}

	//Extract quota, if set
	var quota int64 = -1
	quotaHeader := account.Header.Get("X-Account-Meta-Quota-Bytes")
	if quotaHeader != "" {
		quota, _ = strconv.ParseInt(quotaHeader, 10, 64)
	}

	//Extract usage
	var usage int64
	usageHeader := account.Header.Get("X-Account-Bytes-Used")
	if usageHeader != "" {
		usage, _ = strconv.ParseInt(usageHeader, 10, 64)
	}

	util.LogDebug("Swift Account %s: quota '%d' - usage '%d'", projectUUID, quota, usage)
	return map[string]limes.ResourceData{
		"capacity": limes.ResourceData{
			Quota: quota,
			Usage: uint64(usage),
		},
	}, nil
}

//SetQuota implements the limes.Plugin interface.
func (p *swiftPlugin) SetQuota(driver limes.Driver, domainUUID, projectUUID string, quotas map[string]uint64) error {
	client, err := p.projectClient(driver, projectUUID)
	if err != nil {
		return err
	}

	headers := make(map[string]string)
	headers["X-Account-Meta-Quota-Bytes"] = string(quotas["capacity"])
	//this header brought to you by https://github.com/sapcc/swift-addons
	headers["X-Account-Project-Domain-Id-Override"] = domainUUID

	result, err := updateAccount(client, headers)
	if result.StatusCode == http.StatusNotFound && quotas["capacity"] > 0 {
		//account does not exist yet - if there is a non-zero quota, enable it now
		_, err = putAccount(client, headers)
		if err != nil {
			util.LogInfo("Swift Account %s created", projectUUID)
		}
	}
	return err
}

//Get the project scoped cliet with specific storage URL
func (p *swiftPlugin) projectClient(driver limes.Driver, projectUUID string) (*gophercloud.ServiceClient, error) {
	client, err := p.Client(driver)
	if err != nil {
		return nil, err
	}

	//We act as Reseller_Admin here, but cannot use the endpoint url returned from catalogue
	//Replace the resellers project id with the requested one
	client.Endpoint = urlRegex.ReplaceAllString(client.Endpoint, "${1}"+projectUUID)
	util.LogDebug(client.Endpoint)
	return client, nil
}

//Wrapping the accounts.Get because the swift account might not be created if account_auto_create = false
func getAccount(client *gophercloud.ServiceClient, projectUUID string) *accounts.GetResult {
	//Get account metadata
	var result accounts.GetResult
	result = accounts.Get(client, accounts.GetOpts{})
	if _, ok := result.Err.(gophercloud.ErrDefault404); ok {
		//Swift Account does not exist. This is expected esp. if account_auto_create is disabled
		util.LogDebug("Swift Account %s does not exist", projectUUID)
		return nil
	}
	return &result
}

//Issue a POST request to the account with own headers
func updateAccount(c *gophercloud.ServiceClient, headers map[string]string) (*http.Response, error) {
	return c.Request("POST", c.Endpoint, &gophercloud.RequestOpts{
		MoreHeaders: headers,
		OkCodes:     []int{200, 204},
	})
}

//Issue a PUT request to the account with own headers
func putAccount(c *gophercloud.ServiceClient, headers map[string]string) (*http.Response, error) {
	return c.Request("PUT", c.Endpoint, &gophercloud.RequestOpts{
		MoreHeaders: headers,
		OkCodes:     []int{201},
	})
}
