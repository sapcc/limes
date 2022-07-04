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

package test

import (
	"net/url"
	"testing"

	gorp "gopkg.in/gorp.v2"

	"github.com/sapcc/go-bits/easypg"

	"github.com/sapcc/limes/pkg/db"
)

//InitDatabase initializes DB in pkg/db for testing.
func InitDatabase(t *testing.T, fixtureFile *string) {
	t.Helper()
	//nolint:errcheck
	postgresURL, _ := url.Parse("postgres://postgres:postgres@localhost:54321/limes?sslmode=disable")
	postgresDB, err := easypg.Connect(easypg.Configuration{
		PostgresURL: postgresURL,
		Migrations:  db.SQLMigrations,
	})
	if err != nil {
		t.Error(err)
		t.Log("Try prepending ./testing/with-postgres-db.sh to your command.")
		t.FailNow()
	}

	db.DB = &gorp.DbMap{Db: postgresDB, Dialect: gorp.PostgresDialect{}}

	//reset the DB contents and populate with initial resources if requested
	easypg.ClearTables(t, db.DB.Db, "cluster_capacitors", "cluster_services", "domains") //all other tables via "ON DELETE CASCADE"
	if fixtureFile != nil {
		easypg.ExecSQLFile(t, db.DB.Db, *fixtureFile)
	}
	easypg.ResetPrimaryKeys(t, db.DB.Db, "cluster_services", "domains", "domain_services", "projects", "project_services")

	db.InitGorp()
}

//AssertDBContent makes a dump of the database contents (as a sequence of
//INSERT statements) and runs diff(1) against the given file, producing a test
//error if these two are different from each other.
func AssertDBContent(t *testing.T, fixtureFile string) {
	t.Helper()
	easypg.AssertDBContent(t, db.DB.Db, fixtureFile)
}
