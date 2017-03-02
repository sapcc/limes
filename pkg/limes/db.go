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

package limes

import (
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	//enable postgres driver for database/sql
	_ "github.com/lib/pq"
)

//DB holds the main database connection. It will be `nil` until InitDatabase() is called.
var DB *Database

//InitDatabase initializes the connection to the database.
func InitDatabase(cfg Configuration) error {
	db, err := sql.Open("postgres", cfg.Database.Location)
	if err != nil {
		return err
	}
	DB = &Database{db}

	//wait for database to reach our expected migration level (this is useful
	//because, depending on the rollout strategy, `limes-migrate` might still be
	//running when we are starting, so wait for it to complete)
	migrationLevel, err := getCurrentMigrationLevel(cfg)
	Log(LogDebug, "waiting for database to migrate to schema version %d", migrationLevel)
	if err != nil {
		return err
	}
	stmt, err := DB.Prepare(fmt.Sprintf("SELECT 1 FROM schema_migrations WHERE version = %d", migrationLevel))
	if err != nil {
		return err
	}
	defer stmt.Close()

	waitInterval := 1
	for {
		rows, err := stmt.Query()
		if err != nil {
			return err
		}
		if rows.Next() {
			//got a row - success
			break
		}
		//did not get a row - expected migration not there -> sleep with exponential backoff
		waitInterval *= 2
		Log(LogInfo, "database is not migrated to schema version %d yet - will retry in %d seconds", migrationLevel, waitInterval)
		time.Sleep(time.Duration(waitInterval) * time.Second)
	}

	return nil
}

func getCurrentMigrationLevel(cfg Configuration) (int, error) {
	//list files in migration directory
	dir, err := os.Open(cfg.Database.MigrationsPath)
	if err != nil {
		return 0, err
	}
	fileNames, err := dir.Readdirnames(-1)
	if err != nil {
		return 0, err
	}

	result := 0
	rx := regexp.MustCompile(`^([0-9]+)_.*\.(?:up|down)\.sql`)
	//find the relevant SQL files and extract their migration numbers
	for _, fileName := range fileNames {
		match := rx.FindStringSubmatch(fileName)
		if match != nil {
			migration, _ := strconv.Atoi(match[1])
			if migration > result {
				result = migration
			}
		}
	}

	return result, nil
}

//Database wraps the normal sql.DB structure to provide optional statement tracing.
type Database struct {
	inner *sql.DB
}

//Exec works like for sql.DB.
func (db *Database) Exec(query string, args ...interface{}) (sql.Result, error) {
	traceQuery(query, args)
	return db.inner.Exec(query, args...)
}

//Prepare works like for sql.DB.
func (db *Database) Prepare(query string) (*Statement, error) {
	stmt, err := db.inner.Prepare(query)
	return &Statement{stmt, query}, err
}

//Query works like for sql.DB.
func (db *Database) Query(query string, args ...interface{}) (*sql.Rows, error) {
	traceQuery(query, args)
	return db.inner.Query(query, args...)
}

//QueryRow works like for sql.DB.
func (db *Database) QueryRow(query string, args ...interface{}) *sql.Row {
	traceQuery(query, args)
	return db.inner.QueryRow(query, args...)
}

//Statement wraps the normal sql.Stmt structure to provide optional statement tracing.
type Statement struct {
	inner *sql.Stmt
	query string
}

//Close works like for sql.Stmt.
func (s *Statement) Close() error {
	return s.inner.Close()
}

//Query works like for sql.Stmt.
func (s *Statement) Query(args ...interface{}) (*sql.Rows, error) {
	traceQuery(s.query, args)
	return s.inner.Query(args...)
}

//QueryRow works like for sql.Stmt.
func (s *Statement) QueryRow(query string, args ...interface{}) *sql.Row {
	traceQuery(s.query, args)
	return s.inner.QueryRow(args...)
}

func traceQuery(query string, args []interface{}) {
	if !isDebug {
		return
	}
	if len(args) == 0 {
		Log(LogDebug, query)
		return
	}
	formatStr := strings.Replace(query, "%", "%%", -1) + " ["
	for _ = range args {
		formatStr += "%#v, "
	}
	Log(LogDebug, strings.TrimSuffix(formatStr, ", ")+"]", args...)
}
