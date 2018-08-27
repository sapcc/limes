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
	"testing"

	gorp "gopkg.in/gorp.v2"

	"github.com/sapcc/go-bits/postlite"
	"github.com/sapcc/limes/pkg/db"
)

//InitDatabase initializes DB in pkg/db with an empty in-memory SQLite
//database.
func InitDatabase(t *testing.T) {
	t.Helper()
	sqliteDB, err := postlite.Connect(postlite.Configuration{
		Migrations: db.SQLMigrations,
	})
	if err != nil {
		t.Fatal(err)
	}

	db.DB = &gorp.DbMap{Db: sqliteDB, Dialect: gorp.SqliteDialect{}}
	db.InitGorp()
}

//ExecSQLFile loads a file containing SQL statements and executes them all.
//It implies that every SQL statement is on a single line.
func ExecSQLFile(t *testing.T, path string) {
	t.Helper()
	postlite.ExecSQLFile(t, db.DB.Db, path)
}

//AssertDBContent makes a dump of the database contents (as a sequence of
//INSERT statements) and runs diff(1) against the given file, producing a test
//error if these two are different from each other.
func AssertDBContent(t *testing.T, fixtureFile string) {
	t.Helper()
	postlite.AssertDBContent(t, db.DB.Db, fixtureFile)
}
