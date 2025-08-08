// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package reports

import (
	"database/sql"
	"sort"
	"time"

	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/db"
)

var scrapeErrorsQuery = sqlext.SimplifyWhitespace(`
	SELECT d.uuid, d.name, p.uuid, p.name, cs.type, ps.checked_at, ps.scrape_error_message
	  FROM projects p
	  JOIN domains d ON d.id = p.domain_id
	  JOIN project_services ps ON ps.project_id = p.id
	  JOIN services cs ON cs.id = ps.service_id
	WHERE ps.scrape_error_message != ''
	ORDER BY d.name, p.name, cs.type, ps.scrape_error_message
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
	AffectedProjects int               `json:"affected_projects,omitempty"`
	ServiceType      limes.ServiceType `json:"service_type"`
	CheckedAt        *int64            `json:"checked_at"`
	Message          string            `json:"message"`
}

func GetScrapeErrors(dbi db.Interface, filter Filter) ([]ScrapeError, error) {
	return getScrapeErrors(dbi, filter, scrapeErrorsQuery)
}

func getScrapeErrors(dbi db.Interface, filter Filter, dbQuery string) ([]ScrapeError, error) {
	var result []ScrapeError
	queryStr, joinArgs := filter.PrepareQuery(dbQuery)
	err := sqlext.ForeachRow(dbi, queryStr, joinArgs, func(rows *sql.Rows) error {
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
		// Ensure that empty list gets serialized as `[]` rather than as `null`.
		return []ScrapeError{}, nil
	}

	// To avoid excessively large responses, we group identical scrape errors for multiple
	// project services of the same type into one item.
	uniqueErrors := make(map[limes.ServiceType]map[string]ScrapeError) // second key is error message
	for _, v := range result {
		if vFromMap, found := uniqueErrors[v.ServiceType][v.Message]; found {
			// Use the value from map so we can preserve AffectedProject count.
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
				// If only one project is affected then set to 0 so that this field can
				// omitted in JSON response.
				sErr.AffectedProjects = 0
			}
			result = append(result, sErr)
		}
	}

	// Deterministic ordering for unit tests
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
