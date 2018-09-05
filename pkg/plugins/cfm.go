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
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/util"
)

type cfmPlugin struct {
	cfg limes.ServiceConfiguration

	shareserversCache        []cfmShareserver
	shareserversCacheExpires time.Time
}

func init() {
	limes.RegisterQuotaPlugin(func(c limes.ServiceConfiguration, scrapeSubresources map[string]bool) limes.QuotaPlugin {
		return &cfmPlugin{cfg: c}
	})
}

//Init implements the limes.QuotaPlugin interface.
func (p *cfmPlugin) Init(provider *gophercloud.ProviderClient) error {
	return nil
}

//ServiceInfo implements the limes.QuotaPlugin interface.
func (p *cfmPlugin) ServiceInfo() limes.ServiceInfo {
	return limes.ServiceInfo{
		Type:        "database",
		ProductName: "cfm",
		Area:        "storage",
	}
}

//Resources implements the limes.QuotaPlugin interface.
func (p *cfmPlugin) Resources() []limes.ResourceInfo {
	return []limes.ResourceInfo{{
		Name: "cfm_share_capacity",
		Unit: limes.UnitBytes,
		//we cannot set quota for this service
		ExternallyManaged: true,
	}}
}

//Scrape implements the limes.QuotaPlugin interface.
func (p *cfmPlugin) Scrape(provider *gophercloud.ProviderClient, clusterID, domainUUID, projectUUID string) (map[string]limes.ResourceData, error) {
	//cache the result of cfmListShareservers(), it's mildly expensive
	now := time.Now()
	if p.shareserversCache == nil || p.shareserversCacheExpires.Before(now) {
		shareservers, err := cfmListShareservers(provider)
		if err != nil {
			return nil, err
		}
		p.shareserversCache = shareservers
		p.shareserversCacheExpires = now.Add(5 * time.Minute)
	}
	shareservers := p.shareserversCache

	result := limes.ResourceData{Quota: 0, Usage: 0}
	for _, shareserver := range shareservers {
		if shareserver.ProjectUUID != projectUUID {
			continue
		}

		shareserverDetailed, err := cfmGetShareserver(provider, shareserver.DetailsURL)
		if err != nil {
			return nil, err
		}

		result.Quota += int64(shareserverDetailed.MaximumSizeBytes)
		result.Usage += shareserverDetailed.SizeBytes
	}

	return map[string]limes.ResourceData{"cfm_share_capacity": result}, nil
}

//SetQuota implements the limes.QuotaPlugin interface.
func (p *cfmPlugin) SetQuota(provider *gophercloud.ProviderClient, clusterID, domainUUID, projectUUID string, quotas map[string]uint64) error {
	return errors.New("the database/cfm_share_capacity resource is externally managed")
}

////////////////////////////////////////////////////////////////////////////////

type cfmShareserver struct {
	Type        string
	ProjectUUID string
	DetailsURL  string
	//fields that are only filled by cfmGetShareserver, not by cfmListShareservers
	SizeBytes        uint64
	MaximumSizeBytes uint64
}

func cfmListShareservers(provider *gophercloud.ProviderClient) ([]cfmShareserver, error) {
	baseURL, err := provider.EndpointLocator(gophercloud.EndpointOpts{
		Type:         "database",
		Name:         "cfm",
		Availability: gophercloud.AvailabilityPublic,
	})
	if err != nil {
		return nil, err
	}

	url := strings.TrimSuffix(baseURL, "/") + "/v1.0/shareservers/"
	var data struct {
		Shareservers []struct {
			Links       []gophercloud.Link `json:"links"`
			Type        string             `json:"type"`
			ProjectUUID string             `json:"customer_id"`
		} `json:"shareservers"`
	}
	err = cfmDoRequest(provider, url, &data)
	if err != nil {
		return nil, fmt.Errorf("GET %s failed: %s", url, err.Error())
	}

	result := make([]cfmShareserver, len(data.Shareservers))
	for idx, srv := range data.Shareservers {
		result[idx] = cfmShareserver{
			Type:        srv.Type,
			ProjectUUID: srv.ProjectUUID,
			DetailsURL:  srv.Links[0].Href,
		}
	}
	return result, nil
}

func cfmGetShareserver(provider *gophercloud.ProviderClient, url string) (*cfmShareserver, error) {
	var data struct {
		Shareserver struct {
			Properties struct {
				SizeBytes        util.CFMBytes `json:"size"`
				MaximumSizeBytes util.CFMBytes `json:"maximum_size"`
			} `json:"properties"`
			Links       []gophercloud.Link `json:"links"`
			Type        string             `json:"type"`
			ProjectUUID string             `json:"customer_id"`
		} `json:"shareserver"`
	}
	err := cfmDoRequest(provider, url, &data)
	if err != nil {
		return nil, fmt.Errorf("GET %s failed: %s", url, err.Error())
	}

	srv := data.Shareserver
	return &cfmShareserver{
		Type:             srv.Type,
		ProjectUUID:      srv.ProjectUUID,
		DetailsURL:       srv.Links[0].Href,
		SizeBytes:        uint64(srv.Properties.SizeBytes),
		MaximumSizeBytes: uint64(srv.Properties.MaximumSizeBytes),
	}, nil
}

func cfmDoRequest(provider *gophercloud.ProviderClient, url string, body interface{}) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	token := provider.Token()
	req.Header.Set("Authorization", "Token "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	//success case
	if resp.StatusCode == http.StatusOK {
		return json.NewDecoder(resp.Body).Decode(&body)
	}

	//error case: read error message from body
	buf, err := ioutil.ReadAll(resp.Body)
	if err == nil {
		err = errors.New(string(buf))
	}

	//detect when token has expired
	//
	//NOTE: We don't trust the resp.StatusCode here. The CFM API is known to return
	//403 when it means 401.
	if strings.Contains(err.Error(), "Invalid credentials") {
		err = provider.Reauthenticate(token)
		if err == nil {
			//restart function call after successful reauth
			return cfmDoRequest(provider, url, body)
		}
	}

	return err
}
