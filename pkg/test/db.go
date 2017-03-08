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
	"database/sql"
	"os"
	"testing"

	"github.com/mattes/migrate/migrate"
	"github.com/sapcc/limes/pkg/db"

	//provides sqlite3 database driver
	_ "github.com/mattes/migrate/driver/sqlite3"
	_ "github.com/mattn/go-sqlite3"
)

//InitDatabase initializes DB in pkg/db with an empty in-memory SQLite
//database.
func InitDatabase(t *testing.T, migrationsPath string) {
	//wipe DB (might be left over from previous test runs)
	dbPath := "unittest.db"
	err := os.Remove(dbPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	//apply DB schema
	errs, ok := migrate.UpSync("sqlite3://"+dbPath, migrationsPath)
	if !ok {
		for _, err := range errs {
			t.Error(err)
		}
		t.FailNow()
	}

	//initialize DB connection
	db.DB, err = sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
}
