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

package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/util"
)

func main() {
	//expect three arguments (config file name, cluster ID, URI to Elektra dump endpoint)
	if len(os.Args) != 4 {
		fmt.Fprintf(os.Stderr, "Usage: %s <config-file> <cluster-id> <elektra-dump-url>\n", os.Args[0])
		os.Exit(1)
	}
	config := limes.NewConfiguration(os.Args[1])

	//connect to database
	err := db.Init(config.Database)
	if err != nil {
		util.LogFatal(err.Error())
	}

	//connect to cluster
	cluster, exists := config.Clusters[os.Args[2]]
	if !exists {
		util.LogFatal("no such cluster configured: " + os.Args[2])
	}
	driver, err := limes.NewDriver(cluster)
	if err != nil {
		util.LogFatal(err.Error())
	}

	http.DefaultClient.Transport = http.DefaultTransport
	for {
		util.LogInfo("Starting sync...")
		err = syncWithElektra(driver, os.Args[3], false)
		if err == nil {
			util.LogInfo("Done.")
		} else {
			util.LogError(err.Error())
		}
		time.Sleep(15 * time.Minute)
	}
}

//Entry is an entry in the JSON returned by Elektra.
type Entry struct {
	DomainUUID   string `json:"domain_id"`
	ProjectUUID  string `json:"project_id"`
	ServiceType  string `json:"service"`
	ResourceName string `json:"resource"`
	Quota        uint64 `json:"approved_quota"`
}

func syncWithElektra(driver limes.Driver, elektraURL string, freshToken bool) error {
	//build request
	req, err := http.NewRequest("GET", elektraURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Auth-Token", driver.Client().TokenID)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	//check response
	switch {
	case resp.StatusCode == 200:
		//continue parsing the response below
	case resp.StatusCode == 401 && !freshToken:
		//our token might have expired - fetch a new one and retry
		err := driver.Client().ReauthFunc()
		if err != nil {
			return err
		}
		return syncWithElektra(driver, elektraURL, true)
	default:
		//unexpected response
		return fmt.Errorf("GET expected 200, got %s", resp.Status)
	}

	//parse JSON
	var entries []Entry
	err = json.NewDecoder(resp.Body).Decode(&entries)
	if err != nil {
		return err
	}

	//store data
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer db.RollbackUnlessCommitted(tx)

	stmtProject, err := tx.Prepare(`
		UPDATE project_resources SET quota = $1
		WHERE  name = $2 AND service_id IN (
			SELECT ps.id FROM project_services ps JOIN projects p ON ps.project_id = p.id
			WHERE  ps.type = $3 AND p.uuid = $4
		)
	`)
	if err != nil {
		return err
	}
	defer stmtProject.Close()

	stmtDomain, err := tx.Prepare(`
		SELECT ds.id FROM domain_services ds JOIN domains d ON ds.domain_id = d.id
		WHERE  ds.type = $1 AND d.uuid = $2
	`)
	if err != nil {
		return err
	}
	defer stmtDomain.Close()

	for _, entry := range entries {
		if entry.ProjectUUID != "" {
			_, err = stmtProject.Exec(entry.Quota, entry.ResourceName, entry.ServiceType, entry.ProjectUUID)
			if err != nil {
				return err
			}
			continue
		}

		var serviceID int64
		err = stmtDomain.QueryRow(entry.ServiceType, entry.DomainUUID).Scan(&serviceID)
		if err != nil {
			if err == sql.ErrNoRows {
				//skip services not yet supported by Limes
				continue
			}
			return err
		}

		record := &db.DomainResource{
			ServiceID: serviceID,
			Name:      entry.ResourceName,
			Quota:     entry.Quota,
		}

		rowsAffected, err := tx.Update(record)
		if err != nil {
			return err
		}
		if rowsAffected == 0 {
			err = tx.Insert(record)
			if err != nil {
				return err
			}
		}

	}
	return tx.Commit()
}
