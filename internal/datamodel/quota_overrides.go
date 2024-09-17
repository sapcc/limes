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

package datamodel

import (
	"fmt"
	"os"

	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/errext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

// LoadQuotaOverrides parses the config file at $LIMES_QUOTA_OVERRIDES_PATH.
func LoadQuotaOverrides(c *core.Cluster) (result map[string]map[string]map[db.ServiceType]map[liquid.ResourceName]uint64, errs errext.ErrorSet) {
	path := os.Getenv("LIMES_QUOTA_OVERRIDES_PATH")
	if path == "" {
		return nil, nil
	}
	buf, err := os.ReadFile(path)
	if err != nil {
		errs.Add(err)
		return
	}

	nameMapping := core.BuildResourceNameMapping(c)
	allResInfos := make(map[db.ServiceType]map[liquid.ResourceName]liquid.ResourceInfo, len(c.QuotaPlugins))
	for dbServiceType, quotaPlugin := range c.QuotaPlugins {
		allResInfos[dbServiceType] = quotaPlugin.Resources()
	}

	// the quota-overrides.json file refers to services and resources using IdentityInV1API, so we:
	// a) need to lookup by API identity
	// b) get a result that is structured by API identity and needs to be mapped back to DB identity afterwards
	getUnit := func(serviceType limes.ServiceType, resourceName limesresources.ResourceName) (limes.Unit, error) {
		dbServiceType, dbResourceName, exists := nameMapping.MapFromV1API(serviceType, resourceName)
		if !exists {
			return limes.UnitUnspecified, fmt.Errorf("%s/%s is not a valid resource", serviceType, resourceName)
		}
		resInfo, exists := allResInfos[dbServiceType][dbResourceName]
		if !exists {
			return limes.UnitUnspecified, fmt.Errorf("%s/%s is not a valid resource", serviceType, resourceName)
		}
		if !resInfo.HasQuota {
			return limes.UnitUnspecified, fmt.Errorf("%s/%s does not track quota", serviceType, resourceName)
		}
		return resInfo.Unit, nil
	}
	parsed, suberrs := limesresources.ParseQuotaOverrides(buf, getUnit)
	for _, suberr := range suberrs {
		errs.Addf("while parsing %s: %w", path, suberr)
	}
	if !errs.IsEmpty() {
		return nil, errs
	}

	result = make(map[string]map[string]map[db.ServiceType]map[liquid.ResourceName]uint64, len(parsed))
	for domainName, parsedInDomain := range parsed {
		result[domainName] = make(map[string]map[db.ServiceType]map[liquid.ResourceName]uint64, len(parsedInDomain))
		for projectName, parsedInProject := range parsedInDomain {
			result[domainName][projectName], err = translateQuotaOverrides(parsedInProject, nameMapping)
			if err != nil {
				errs.Add(err)
				return nil, errs
			}
		}
	}
	return result, nil
}

func translateQuotaOverrides(overrides map[limes.ServiceType]map[limesresources.ResourceName]uint64, nameMapping core.ResourceNameMapping) (map[db.ServiceType]map[liquid.ResourceName]uint64, error) {
	result := make(map[db.ServiceType]map[liquid.ResourceName]uint64)
	for apiServiceType, overridesByService := range overrides {
		for apiResourceName, overrideQuota := range overridesByService {
			dbServiceType, dbResourceName, exists := nameMapping.MapFromV1API(apiServiceType, apiResourceName)
			if !exists {
				// defense in depth: this branch should be impossible to reach because ParseQuotaOverrides() rejected unknown resources
				return nil, fmt.Errorf("%s/%s is not a valid resource", apiServiceType, apiResourceName)
			}

			if result[dbServiceType] == nil {
				result[dbServiceType] = make(map[liquid.ResourceName]uint64)
			}
			result[dbServiceType][dbResourceName] = overrideQuota
		}
	}
	return result, nil
}
