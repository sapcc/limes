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
	"database/sql"
	"fmt"

	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

var (
	backendQuotaSelectQuery = sqlext.SimplifyWhitespace(`
		SELECT name, backend_quota, desired_backend_quota
		  FROM project_resources
		 WHERE service_id = $1 AND desired_backend_quota IS NOT NULL
	`)
	backendQuotaMarkAsAppliedQuery = sqlext.SimplifyWhitespace(`
		UPDATE project_resources
		   SET backend_quota = desired_backend_quota
		 WHERE service_id = $1
	`)
)

// ApplyBackendQuota applies the backend quota for the given project service.
// The caller must ensure that the given service is in the given project is in
// the given domain is in the given cluster.
//
// If the backend quotas recorded in the project service's resources already
// match the expected values, nothing is done.
//
// This function must be called after each ProjectResourceUpdate.Run(). It is
// not called by Run() because the caller will usually want to commit the DB
// transaction before calling out into the backend.
func ApplyBackendQuota(dbi db.Interface, cluster *core.Cluster, domain core.KeystoneDomain, project db.Project, srv db.ProjectServiceRef) error {
	plugin := cluster.QuotaPlugins[srv.Type]
	if plugin == nil {
		return fmt.Errorf("no quota plugin registered for service type %s", srv.Type)
	}

	//collect backend quota values that we want to apply
	targetQuotasInDB := make(map[string]uint64)
	needsApply := false
	err := sqlext.ForeachRow(dbi, backendQuotaSelectQuery, []any{srv.ID}, func(rows *sql.Rows) error {
		var (
			resourceName string
			currentQuota *int64
			targetQuota  uint64
		)
		err := rows.Scan(&resourceName, &currentQuota, &targetQuota)
		if err != nil {
			return err
		}
		targetQuotasInDB[resourceName] = targetQuota
		if currentQuota == nil || *currentQuota < 0 || uint64(*currentQuota) != targetQuota {
			needsApply = true
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("while collecting target quota values for %s backend: %w", srv.Type, err)
	}

	//if everything is looking good already, we're done here
	if !needsApply {
		return nil
	}

	//double-check that we only include quota values for resources that the backend currently knows about
	targetQuotasForBackend := make(map[string]uint64)
	for _, res := range plugin.Resources() {
		if res.NoQuota {
			continue
		}
		//NOTE: If `targetQuotasInDB` does not have an entry for this resource, we will write 0 into the backend.
		targetQuotasForBackend[res.Name] = targetQuotasInDB[res.Name]
	}

	//apply quotas in backend
	err = plugin.SetQuota(core.KeystoneProjectFromDB(project, domain), targetQuotasForBackend)
	if err != nil {
		return err
	}
	_, err = dbi.Exec(backendQuotaMarkAsAppliedQuery, srv.ID)
	return err
}
