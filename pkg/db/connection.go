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
	"errors"
	"net/url"

	gorp "gopkg.in/gorp.v2"

	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/postlite"
)

//DB holds the main database connection. It will be `nil` until InitDatabase() is called.
var DB *gorp.DbMap

//Configuration is the section of the global configuration file that
//contains the connection info for the Postgres database.
type Configuration struct {
	Location string `yaml:"location"`
}

//Init initializes the connection to the database.
func Init(cfg Configuration) error {
	pgURL, err := url.Parse(cfg.Location)
	if err != nil {
		return errors.New("malformed URL in database.location: " + err.Error())
	}

	db, err := postlite.Connect(postlite.Configuration{
		PostgresURL: pgURL,
		Migrations:  SQLMigrations,
	})
	if err != nil {
		return err
	}
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
	Exec(query string, args ...interface{}) (sql.Result, error)
	Prepare(query string) (*sql.Stmt, error)
	Query(query string, args ...interface{}) (*sql.Rows, error)
	QueryRow(query string, args ...interface{}) *sql.Row
}
