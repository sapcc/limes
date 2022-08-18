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

package reports

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/pkg/core"
	"github.com/sapcc/limes/pkg/db"
)

var scrapeErrorsQuery = sqlext.SimplifyWhitespace(`
	SELECT d.uuid, d.name, p.uuid, p.name, ps.type, ps.checked_at, ps.scrape_error_message
	  FROM projects p
	  JOIN domains d ON d.id = p.domain_id
	  JOIN project_services ps ON ps.project_id = p.id
	WHERE %s AND ps.scrape_error_message != ''
	ORDER BY d.name, p.name, ps.type, ps.scrape_error_message
`)

type ScrapeError struct {
	Project struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Domain struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"domain"`
	} `json:"project"`
	AffectedProjects int    `json:"affected_projects,omitempty"`
	ServiceType      string `json:"service_type"`
	CheckedAt        *int64 `json:"checked_at"`
	Message          string `json:"message"`
}

func GetScrapeErrors(cluster *core.Cluster, dbi db.Interface, filter Filter) ([]ScrapeError, error) {
	return getScrapeErrors(cluster, dbi, filter, scrapeErrorsQuery)
}

func GetRateScrapeErrors(cluster *core.Cluster, dbi db.Interface, filter Filter) ([]ScrapeError, error) {
	dbQuery := strings.ReplaceAll(scrapeErrorsQuery, "scrape_error_message", "rates_scrape_error_message")
	dbQuery = strings.ReplaceAll(dbQuery, "checked_at", "rates_checked_at")
	return getScrapeErrors(cluster, dbi, filter, dbQuery)
}

func getScrapeErrors(cluster *core.Cluster, dbi db.Interface, filter Filter, dbQuery string) ([]ScrapeError, error) {
	fields := map[string]interface{}{"d.cluster_id": cluster.ID}

	var result []ScrapeError
	queryStr, joinArgs := filter.PrepareQuery(dbQuery)
	whereStr, whereArgs := db.BuildSimpleWhereClause(fields, len(joinArgs))
	err := sqlext.ForeachRow(dbi, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
		var sErr ScrapeError
		var checkedAtAsTime *time.Time
		err := rows.Scan(
			&sErr.Project.Domain.ID, &sErr.Project.Domain.Name, &sErr.Project.ID,
			&sErr.Project.Name, &sErr.ServiceType, &checkedAtAsTime, &sErr.Message,
		)
		if err != nil {
			return err
		}

		if checkedAtAsTime != nil {
			v := checkedAtAsTime.Unix()
			sErr.CheckedAt = &v
		}
		result = append(result, sErr)

		return nil
	})
	if err != nil {
		return nil, err
	}

	if len(result) == 0 {
		//Ensure that empty list gets serialized as `[]` rather than as `null`.
		return []ScrapeError{}, nil
	}

	//To avoid excessively large responses, we group identical scrape errors for multiple
	//project services of the same type into one item.
	uniqueErrors := make(map[string]map[string]ScrapeError) //map[serviceType]map[Message]ScrapeError
	for _, v := range result {
		if vFromMap, found := uniqueErrors[v.ServiceType][v.Message]; found {
			//Use the value from map so we can preserve AffectedProject count.
			v = vFromMap
		}
		if _, ok := uniqueErrors[v.ServiceType]; !ok {
			uniqueErrors[v.ServiceType] = make(map[string]ScrapeError)
		}
		v.AffectedProjects++
		uniqueErrors[v.ServiceType][v.Message] = v
	}

	result = nil
	for _, errMsgs := range uniqueErrors {
		for _, sErr := range errMsgs {
			if sErr.AffectedProjects == 1 {
				//If only one project is affected then set to 0 so that this field can
				//omitted in JSON response.
				sErr.AffectedProjects = 0
			}
			result = append(result, sErr)
		}
	}

	//Deterministic ordering for unit tests
	sort.Slice(result, func(i, j int) bool {
		srvType1 := result[i].ServiceType
		srvType2 := result[j].ServiceType
		if srvType1 != srvType2 {
			return srvType1 < srvType2
		}
		pID1 := result[i].Project.ID
		pID2 := result[j].Project.ID
		return pID1 < pID2
	})

	return result, nil
}
