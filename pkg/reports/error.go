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

	"github.com/sapcc/limes/pkg/core"
	"github.com/sapcc/limes/pkg/db"
)

var scrapeErrorsQuery = db.SimplifyWhitespaceInSQL(`
	SELECT d.uuid, d.name, p.uuid, p.name, ps.type, ps.checked_at, ps.scrape_error_message
	  FROM projects p
	  LEFT OUTER JOIN domains d ON d.id = p.domain_id
	  LEFT OUTER JOIN project_services ps ON ps.project_id = p.id
	WHERE %s GROUP BY d.uuid, d.name, p.uuid, p.name, ps.type, ps.checked_at, ps.scrape_error_message
	HAVING ps.scrape_error_message != ''
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
	ServiceType string `json:"service_type"`
	CheckedAt   *int64 `json:"checked_at"`
	Message     string `json:"message"`
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
	err := db.ForeachRow(dbi, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
		sErr := ScrapeError{}
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
	//project services of the same type into one item with a dummy project ID.
	uniqueErrors := make(map[string]map[string]ScrapeError) //map[serviceType]map[Message]ScrapeError
	for _, v := range result {
		if _, found := uniqueErrors[v.ServiceType][v.Message]; found {
			if v.Project.Name == "dummy-project" {
				continue
			}
			v.Project.ID = "uuid-for-dummy-project"
			v.Project.Name = "dummy-project"
			v.Project.Domain.ID = "uuid-for-dummy-domain"
			v.Project.Domain.Name = "dummy-domain"
		}

		if _, ok := uniqueErrors[v.ServiceType]; !ok {
			uniqueErrors[v.ServiceType] = make(map[string]ScrapeError)
		}
		uniqueErrors[v.ServiceType][v.Message] = v
	}
	var new []ScrapeError
	for _, errMsgs := range uniqueErrors {
		for _, sErr := range errMsgs {
			new = append(new, sErr)
		}
	}
	//Deterministic ordering for unit tests
	sort.Slice(new, func(i, j int) bool {
		srvType1 := new[i].ServiceType
		srvType2 := new[j].ServiceType
		if srvType1 != srvType2 {
			return srvType1 < srvType2
		}
		pID1 := new[i].Project.ID
		pID2 := new[i].Project.ID
		return pID1 < pID2
	})
	result = new

	return result, nil
}
