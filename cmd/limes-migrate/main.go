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
	"fmt"
	"net/url"
	"os"
	"regexp"

	_ "github.com/mattes/migrate/driver/postgres"
	"github.com/mattes/migrate/migrate"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/util"
)

func main() {
	//expect one argument (config file name)
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <config-file>\n", os.Args[0])
		os.Exit(1)
	}
	config := limes.NewConfiguration(os.Args[1])

	err := createDatabaseIfNotExist(config)
	if err != nil {
		util.LogError(err.Error())
	}

	errs, ok := migrate.UpSync(config.Database.Location, config.Database.MigrationsPath)
	if !ok {
		util.LogError("migration failed, see errors on stderr")
		for _, err := range errs {
			fmt.Fprintln(os.Stderr, err.Error())
		}
	}

}

var dbNotExistErrRx = regexp.MustCompile(`^pq: database "([^"]+)" does not exist$`)

func createDatabaseIfNotExist(config limes.Configuration) error {
	//check if the database exists
	db, err := sql.Open("postgres", config.Database.Location)
	if err == nil {
		//apparently the "database does not exist" error only occurs when trying to issue the first statement
		_, err = db.Exec("SELECT 1")
	}
	if err == nil {
		//nothing to do
		return db.Close()
	}
	match := dbNotExistErrRx.FindStringSubmatch(err.Error())
	if match == nil {
		//unexpected error
		return err
	}
	dbName := match[1]

	//remove the database name from the connection URL
	dbURL, err := url.Parse(config.Database.Location)
	if err != nil {
		return err
	}

	dbURL.Path = "/"
	db, err = sql.Open("postgres", dbURL.String())
	if err != nil {
		return err
	}
	defer db.Close()

	_, err = db.Exec("CREATE DATABASE " + dbName)
	return err
}
