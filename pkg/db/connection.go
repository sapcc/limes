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

package db

import (
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	gorp "gopkg.in/gorp.v2"

	"github.com/majewsky/sqlproxy"
	"github.com/mattes/migrate"
	bindata "github.com/mattes/migrate/source/go-bindata"
	"github.com/sapcc/limes/pkg/dbdata"
	"github.com/sapcc/limes/pkg/util"
	//enable postgres driver for database/sql
	_ "github.com/lib/pq"
)

//DB holds the main database connection. It will be `nil` until InitDatabase() is called.
var DB *gorp.DbMap

func init() {
	sql.Register("postgres-debug", &sqlproxy.Driver{
		ProxiedDriverName: "postgres",
		BeforeQueryHook:   traceQuery,
	})
	//this driver only used within unit tests
	sql.Register("sqlite3-debug", &sqlproxy.Driver{
		ProxiedDriverName: "sqlite3-limes", //this driver is defined in pkg/test/db.go
		BeforeQueryHook:   traceQuery,
		BeforePrepareHook: func(query string) (string, error) {
			//rewrite Postgres function name into SQLite function name
			query = regexp.MustCompile(`\bGREATEST\b`).ReplaceAllString(query, "MAX")
			//Postgres is okay with a no-op "WHERE TRUE" clause, but SQLite does not know the TRUE literal
			query = regexp.MustCompile(`\bWHERE TRUE\s*(GROUP|LIMIT|ORDER|$)`).ReplaceAllString(query, "$1")
			query = regexp.MustCompile(`\bWHERE TRUE AND\b`).ReplaceAllString(query, "WHERE")
			// traceQuery(query, []interface{}{"PREPARE"})
			return query, nil
		},
	})
}

//Configuration is the section of the global configuration file that
//contains the data about
type Configuration struct {
	Location string `yaml:"location"`
}

//Init initializes the connection to the database.
func Init(cfg Configuration) error {
	sqlDriver := "postgres"
	if os.Getenv("LIMES_DEBUG_SQL") == "1" {
		util.LogInfo("Enabling SQL tracing... \x1B[1;31mTHIS VOIDS YOUR WARRANTY!\x1B[0m If database queries fail in unexpected ways, check first if the tracing causes the issue.")
		sqlDriver += "-debug"
	}

	db, err := sql.Open(sqlDriver, cfg.Location)
	if err != nil {
		return err
	}
	DB = &gorp.DbMap{Db: db, Dialect: gorp.PostgresDialect{}}
	InitGorp()

	//wait for database to reach our expected migration level (this is useful
	//because, depending on the rollout strategy, `limes-migrate` might still be
	//running when we are starting, so wait for it to complete)
	migrationLevel, err := getCurrentMigrationLevel(cfg)
	util.LogDebug("waiting for database to migrate to schema version %d", migrationLevel)
	if err != nil {
		return err
	}
	stmt, err := DB.Prepare(fmt.Sprintf("SELECT 1 FROM schema_migrations WHERE version = %d AND NOT dirty", migrationLevel))
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
		util.LogInfo("database is not migrated to schema version %d yet - will retry in %d seconds", migrationLevel, waitInterval)
		time.Sleep(time.Duration(waitInterval) * time.Second)
	}

	util.LogDebug("database is migrated - commencing normal startup...")
	return nil
}

//GetMigrate returns a github.com/mattes/migrate.Migrate instance for the Limes DB.
//During tests, give the path to the SQLite-compatible migrations (at
//pkg/test/migrations in the repo). At runtime, give "go-bindata" to use
//the compiled-in Postgres-compatible migrations.
func (cfg Configuration) GetMigrate(migrationsPath string) (*migrate.Migrate, error) {
	if migrationsPath != "go-bindata" {
		return migrate.New("file://"+migrationsPath, cfg.Location)
	}

	driver, err := bindata.WithInstance(bindata.Resource(dbdata.AssetNames(), dbdata.Asset))
	if err != nil {
		return nil, err
	}
	return migrate.NewWithSourceInstance("go-bindata", driver, cfg.Location)
}

func getCurrentMigrationLevel(cfg Configuration) (int, error) {
	result := 0
	rx := regexp.MustCompile(`^([0-9]+)_.*\.(?:up|down)\.sql`)
	//find the relevant SQL files and extract their migration numbers
	for _, fileName := range dbdata.AssetNames() {
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

var sqlWhitespaceRx = regexp.MustCompile(`(?:\s|--.*)+`) // `.*` matches until end of line!

func traceQuery(query string, args []interface{}) {
	//simplify query string - remove comments and reduce whitespace
	//(This logic assumes that there are no arbitrary strings in the SQL
	//statement, which is okay since values should be given as args anyway.)
	query = strings.TrimSpace(sqlWhitespaceRx.ReplaceAllString(query, " "))

	//early exit for easy option
	if len(args) == 0 {
		util.LogDebug(query)
		return
	}

	//if args contains time.Time objects, pretty-print these; use
	//fmt.Sprintf("%#v") for all other types of values
	argStrings := make([]string, len(args))
	for idx, argument := range args {
		switch arg := argument.(type) {
		case time.Time:
			argStrings[idx] = "time.Time [" + arg.Local().String() + "]"
		default:
			argStrings[idx] = fmt.Sprintf("%#v", arg)
		}
	}
	util.LogDebug(query + " [" + strings.Join(argStrings, ", ") + "]")
}

//RollbackUnlessCommitted calls Rollback() on a transaction if it hasn't been
//committed or rolled back yet. Use this with the defer keyword to make sure
//that a transaction is automatically rolled back when a function fails.
func RollbackUnlessCommitted(tx *gorp.Transaction) {
	err := tx.Rollback()
	switch err {
	case nil:
		//rolled back successfully
		util.LogInfo("implicit rollback done")
		return
	case sql.ErrTxDone:
		//already committed or rolled back - nothing to do
		return
	default:
		util.LogError("implicit rollback failed: %s", err.Error())
	}
}

//Interface provides the common methods that both SQL connections and
//transactions implement.
type Interface interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
	Prepare(query string) (*sql.Stmt, error)
	Query(query string, args ...interface{}) (*sql.Rows, error)
	QueryRow(query string, args ...interface{}) *sql.Row
}
