/*******************************************************************************
*
* Copyright 2017-2018 SAP SE
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
	"net"
	"net/url"
	"os"

	gorp "gopkg.in/gorp.v2"

	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/limes/pkg/util"
)

//DB holds the main database connection. It will be `nil` until either Init() or
//test.InitDatabase() is called.
var DB *gorp.DbMap

//Init initializes the connection to the database.
func Init() error {
	username := util.EnvOrDefault("LIMES_DB_USERNAME", "postgres")
	pass := os.Getenv("LIMES_DB_PASSWORD")
	host := util.EnvOrDefault("LIMES_DB_HOSTNAME", "localhost")
	port := util.EnvOrDefault("LIMES_DB_PORT", "5432")
	name := util.EnvOrDefault("LIMES_DB_NAME", "limes")

	connOpts, err := url.ParseQuery(os.Getenv("LIMES_DB_CONNECTION_OPTIONS"))
	if err != nil {
		return fmt.Errorf("while parsing LIMES_DB_CONNECTION_OPTIONS: %w", err)
	}
	hostname, err := os.Hostname()
	if err == nil {
		connOpts.Set("application_name", fmt.Sprintf("%s@%s", util.Component, hostname))
	} else {
		connOpts.Set("application_name", util.Component)
	}

	dbURL := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(username, pass),
		Host:     net.JoinHostPort(host, port),
		Path:     name,
		RawQuery: connOpts.Encode(),
	}

	db, err := easypg.Connect(easypg.Configuration{
		PostgresURL: dbURL,
		Migrations:  SQLMigrations,
	})
	if err != nil {
		return err
	}

	//ensure that this process does not starve other Limes processes for DB connections
	db.SetMaxOpenConns(16)

	DB = &gorp.DbMap{Db: db, Dialect: gorp.PostgresDialect{}}
	InitGorp()
	return nil
}

//RollbackUnlessCommitted calls Rollback() on a transaction if it hasn't been
//committed or rolled back yet. Use this with the defer keyword to make sure
//that a transaction is automatically rolled back when a function fails.
func RollbackUnlessCommitted(tx *gorp.Transaction) {
	err := tx.Rollback()
	switch err {
	case nil:
		//rolled back successfully
		logg.Info("implicit rollback done")
		return
	case sql.ErrTxDone:
		//already committed or rolled back - nothing to do
		return
	default:
		logg.Error("implicit rollback failed: %s", err.Error())
	}
}

//Interface provides the common methods that both SQL connections and
//transactions implement.
type Interface interface {
	//from database/sql
	Exec(query string, args ...interface{}) (sql.Result, error)
	Prepare(query string) (*sql.Stmt, error)
	Query(query string, args ...interface{}) (*sql.Rows, error)
	QueryRow(query string, args ...interface{}) *sql.Row
	Insert(args ...interface{}) error

	//from gorp.v2
	Select(i interface{}, query string, args ...interface{}) ([]interface{}, error)
}
