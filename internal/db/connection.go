// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"database/sql"
	"os"

	"github.com/dlmiddlecote/sqlstats"
	gorp "github.com/go-gorp/gorp/v3"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/sapcc/go-api-declarations/bininfo"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/osext"
	"github.com/sapcc/go-bits/sqlext"
)

// Configuration returns the easypg.Configuration object that func Init() needs to initialize the DB connection.
func Configuration() easypg.Configuration {
	return easypg.Configuration{
		Migrations: sqlMigrations,
	}
}

// Init initializes the connection to the database.
func Init() (*sql.DB, error) {
	extraConnectionOptions := make(map[string]string)
	if bininfo.Component() == "limes-serve" {
		// the API seems to have issues with connections getting stuck in "idle in transaction" during high load, not sure yet why
		extraConnectionOptions["idle_in_transaction_session_timeout"] = "10000" // 10000 ms = 10 seconds
	}

	dbName := osext.GetenvOrDefault("LIMES_DB_NAME", "limes")
	dbURL, err := easypg.URLFrom(easypg.URLParts{
		HostName:          osext.GetenvOrDefault("LIMES_DB_HOSTNAME", "localhost"),
		Port:              osext.GetenvOrDefault("LIMES_DB_PORT", "5432"),
		UserName:          osext.GetenvOrDefault("LIMES_DB_USERNAME", "postgres"),
		Password:          os.Getenv("LIMES_DB_PASSWORD"),
		ConnectionOptions: os.Getenv("LIMES_DB_CONNECTION_OPTIONS"),
		DatabaseName:      dbName,
	})
	if err != nil {
		return nil, err
	}
	dbConn, err := easypg.Connect(dbURL, Configuration())
	if err != nil {
		return nil, err
	}
	prometheus.MustRegister(sqlstats.NewStatsCollector(dbName, dbConn))
	return dbConn, nil
}

// InitORM wraps a database connection into a gorp.DbMap instance.
func InitORM(dbConn *sql.DB) *gorp.DbMap {
	// ensure that this process does not starve other Limes processes for DB connections
	dbConn.SetMaxOpenConns(16)

	dbMap := &gorp.DbMap{Db: dbConn, Dialect: gorp.PostgresDialect{}}
	initGorp(dbMap)
	return dbMap
}

// Interface provides the common methods that both SQL connections and
// transactions implement.
type Interface interface {
	// from database/sql
	sqlext.Executor

	// from github.com/go-gorp/gorp
	Insert(args ...any) error
	Update(args ...any) (int64, error)
	Delete(args ...any) (int64, error)
	Select(i any, query string, args ...any) ([]any, error)
}
