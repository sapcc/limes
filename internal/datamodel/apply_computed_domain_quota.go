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

package datamodel

import (
	"database/sql"
	"fmt"

	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

var recomputeDomainQuotaQueryStr = sqlext.SimplifyWhitespace(`
	UPDATE domain_resources dr SET quota = COALESCE((
		SELECT SUM(pr.quota)
		  FROM projects p
		  JOIN project_services ps ON ps.project_id = p.id
		  JOIN project_resources pr ON pr.service_id = ps.id
		 WHERE p.domain_id = $1 AND ps.type = $2 AND pr.name = $3
	), 0)
	WHERE dr.name = $3 AND dr.service_id = (
		SELECT id
		  FROM domain_services ds
		 WHERE ds.domain_id = $1 AND ds.type = $2
	)
`)

// ApplyComputedDomainQuota reevaluates auto-computed domain quotas in the given domain service.
// This is only relevant for resources with non-hierarchical quota distribution, since those resources will have their domain
// quota always set equal to the sum of all respective project quotas.
func ApplyComputedDomainQuota(dbi db.Interface, cluster *core.Cluster, domainID db.DomainID, serviceType string) error {
	plugin := cluster.QuotaPlugins[serviceType]
	if plugin == nil {
		return fmt.Errorf("no quota plugin registered for service type %s", serviceType)
	}

	//check which resources need to have their domain quota recomputed
	var cqdResourceNames []string
	for _, res := range plugin.Resources() {
		qdConfig := cluster.QuotaDistributionConfigForResource(serviceType, res.Name)
		if qdConfig.Model != limesresources.HierarchicalQuotaDistribution {
			cqdResourceNames = append(cqdResourceNames, res.Name)
		}
	}
	if len(cqdResourceNames) == 0 {
		return nil //nothing to do
	}

	return sqlext.WithPreparedStatement(dbi, recomputeDomainQuotaQueryStr, func(stmt *sql.Stmt) error {
		for _, resName := range cqdResourceNames {
			_, err := stmt.Exec(domainID, serviceType, resName)
			if err != nil {
				return err
			}
		}
		return nil
	})
}
