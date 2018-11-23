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

package datamodel

import (
	"fmt"

	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/limes"
)

//ApplyBackendQuota applies the backend quota for the given project service.
//The caller must ensure that the given service is in the given project is in
//the given domain is in the given cluster.
//
//If the backend quotas recorded in the project service's resources already
//match the expected values, nothing is done.
func ApplyBackendQuota(dbi db.Interface, cluster *limes.Cluster, domainUUID, projectUUID string, serviceID int64, serviceType string) error {
	plugin := cluster.QuotaPlugins[serviceType]
	if plugin == nil {
		return fmt.Errorf("no quota plugin registered for service type %s", serviceType)
	}

	var resources []db.ProjectResource
	_, err := dbi.Select(&resources, `SELECT * FROM project_resources WHERE service_id = $1`, serviceID)
	if err != nil {
		return err
	}

	//collect desired backend quotas
	var resourcesToUpdate []db.ProjectResource
	quotaValues := make(map[string]uint64)
	for _, res := range resources {
		desiredQuota := cluster.Config.Bursting.MaxMultiplier.ApplyTo(res.Quota)
		logg.Info("desired %s/%s is %d", serviceType, res.Name, desiredQuota)
		quotaValues[res.Name] = desiredQuota

		if res.BackendQuota < 0 || desiredQuota != uint64(res.BackendQuota) {
			logg.Info("backend quota %s/%s is %d", serviceType, res.Name, res.BackendQuota)
			res.BackendQuota = int64(desiredQuota)
			resourcesToUpdate = append(resourcesToUpdate, res)
		}
	}
	if len(resourcesToUpdate) == 0 {
		return nil
	}

	//apply quotas in backend
	provider, eo := cluster.ProviderClientForService(serviceType)
	err = plugin.SetQuota(provider, eo, cluster.ID, domainUUID, projectUUID, quotaValues)
	if err != nil {
		return err
	}

	//save applied backend quotas in DB
	for _, v := range resourcesToUpdate {
		logg.Info("%#v", v)
	}
	//NOTE: cannot use UpdateColumns() because of https://github.com/go-gorp/gorp/issues/325
	stmt, err := dbi.Prepare(`UPDATE project_resources SET backend_quota = $1 WHERE service_id = $2 AND name = $3`)
	if err != nil {
		return err
	}
	for _, res := range resourcesToUpdate {
		_, err := stmt.Exec(res.BackendQuota, serviceID, res.Name)
		if err != nil {
			return err
		}
	}
	return nil
}
