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
	"net/url"
	"os"

	gorp "github.com/go-gorp/gorp/v3"

	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/osext"
	"github.com/sapcc/go-bits/sqlext"
)

// Init initializes the connection to the database.
func Init() (*gorp.DbMap, error) {
	dbURL, err := easypg.URLFrom(easypg.URLParts{
		HostName:          osext.GetenvOrDefault("LIMES_DB_HOSTNAME", "localhost"),
		Port:              osext.GetenvOrDefault("LIMES_DB_PORT", "5432"),
		UserName:          osext.GetenvOrDefault("LIMES_DB_USERNAME", "postgres"),
		Password:          os.Getenv("LIMES_DB_PASSWORD"),
		ConnectionOptions: os.Getenv("LIMES_DB_CONNECTION_OPTIONS"),
		DatabaseName:      osext.GetenvOrDefault("LIMES_DB_NAME", "limes"),
	})
	if err != nil {
		return nil, err
	}
	return InitFromURL(dbURL)
}

// InitFromURL is like Init, but takes an explicit URL. This is used to
// override the default database URL configuration in tests.
func InitFromURL(dbURL *url.URL) (*gorp.DbMap, error) {
	db, err := easypg.Connect(easypg.Configuration{
		PostgresURL: dbURL,
		Migrations:  sqlMigrations,
	})
	if err != nil {
		return nil, err
	}

	//ensure that this process does not starve other Limes processes for DB connections
	db.SetMaxOpenConns(16)

	dbMap := &gorp.DbMap{Db: db, Dialect: gorp.PostgresDialect{}}
	initGorp(dbMap)
	return dbMap, nil
}

// Interface provides the common methods that both SQL connections and
// transactions implement.
type Interface interface {
	//from database/sql
	sqlext.Executor

	//from github.com/go-gorp/gorp
	Insert(args ...interface{}) error
	Update(args ...interface{}) (int64, error)
	Select(i interface{}, query string, args ...interface{}) ([]interface{}, error)
}
