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

package drivers

import (
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/limits"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/quotasets"
	"github.com/sapcc/limes/pkg/limes"
)

func (d realDriver) novaClient() (*gophercloud.ServiceClient, error) {
	return openstack.NewComputeV2(d.Client,
		gophercloud.EndpointOpts{Availability: gophercloud.AvailabilityPublic},
	)
}

//GetComputeQuota implements the limes.Driver interface.
func (d realDriver) GetComputeQuota(projectUUID string) (limes.ComputeData, error) {
	client, err := d.novaClient()
	if err != nil {
		return limes.ComputeData{}, err
	}

	quotas, err := quotasets.Get(client, projectUUID).Extract()
	if err != nil {
		return limes.ComputeData{}, err
	}

	return limes.ComputeData{
		Cores:     int64(quotas.Cores),
		Instances: int64(quotas.Instances),
		RAM:       int64(quotas.Ram),
	}, nil
}

//GetComputeUsage implements the limes.Driver interface.
func (d realDriver) GetComputeUsage(projectUUID string) (limes.ComputeData, error) {
	client, err := d.novaClient()
	if err != nil {
		return limes.ComputeData{}, err
	}

	limits, err := limits.Get(client, limits.GetOpts{TenantID: projectUUID}).Extract()
	if err != nil {
		return limes.ComputeData{}, err
	}

	return limes.ComputeData{
		Cores:     int64(limits.Absolute.TotalCoresUsed),
		Instances: int64(limits.Absolute.TotalInstancesUsed),
		RAM:       int64(limits.Absolute.TotalRAMUsed),
	}, nil
}
