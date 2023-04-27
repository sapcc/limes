/*******************************************************************************
*
* Copyright 2017-2020 SAP SE
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

package test

import (
	"github.com/gophercloud/gophercloud"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"

	"github.com/sapcc/limes/internal/test/plugins"
)

// NewDiscoveryPlugin creates a DiscoveryPlugin for tests.
func NewDiscoveryPlugin() *plugins.StaticDiscoveryPlugin {
	p := &plugins.StaticDiscoveryPlugin{}
	_ = p.Init(nil, gophercloud.EndpointOpts{}) //nolint:errcheck // implementation never returns an error
	return p
}

// NewPlugin creates a new Plugin for the given service type.
func NewPlugin(rates ...limesrates.RateInfo) *plugins.GenericQuotaPlugin {
	p := &plugins.GenericQuotaPlugin{StaticRateInfos: rates}
	_ = p.Init(nil, gophercloud.EndpointOpts{}, nil) //nolint:errcheck // implementation never returns an error
	return p
}

// NewCapacityPlugin creates a new CapacityPlugin.
func NewCapacityPlugin(resources ...string) *plugins.StaticCapacityPlugin {
	return &plugins.StaticCapacityPlugin{Resources: resources, Capacity: 42}
}
