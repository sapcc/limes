// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/dlmiddlecote/sqlstats"
	"github.com/prometheus/client_golang/prometheus"
	"go.xyrillian.de/gg/gsql"
	"go.xyrillian.de/gg/pgruntime"

	"github.com/sapcc/go-bits/must"
	"github.com/sapcc/go-bits/osext"
	"github.com/sapcc/go-bits/sqlext"
	"github.com/sapcc/go-bits/syncext"

	// import DB driver
	_ "github.com/lib/pq"
)

// Configuration returns the [pgruntime.ConnectionBehavior] object that func Init() needs to initialize the DB connection.
func Configuration() pgruntime.ConnectionBehavior {
	return pgruntime.ConnectionBehavior{
		Migrations: sqlMigrations,
	}
}

// Init initializes the connection to the database.
func Init(ctx context.Context) (*gsql.DB, pgruntime.ConnectionTarget, error) {
	target := pgruntime.ConnectionTarget{
		HostName:          osext.GetenvOrDefault("LIMES_DB_HOSTNAME", "localhost"),
		Port:              osext.GetenvOrDefault("LIMES_DB_PORT", "5432"),
		UserName:          osext.GetenvOrDefault("LIMES_DB_USERNAME", "postgres"),
		Password:          os.Getenv("LIMES_DB_PASSWORD"),
		ConnectionOptions: os.Getenv("LIMES_DB_CONNECTION_OPTIONS"),
		DatabaseName:      osext.GetenvOrDefault("LIMES_DB_NAME", "limes"),
	}
	dbConn := must.Return(pgruntime.StdConnector("postgres").Connect(ctx, target, Configuration()))
	prometheus.MustRegister(sqlstats.NewStatsCollector(target.DatabaseName, dbConn))

	// ensure that this process does not starve other Limes processes for DB connections
	dbConn.SetMaxOpenConns(16)

	return dbConn, target, nil
}

// Interface is implemented by both [*gsql.DB] and [*gsql.Tx].
// We are using this interface in function signatures instead of [gsql.Handle] to allow compatibility with go-bits/sqlext methods.
type Interface interface {
	gsql.Handle
	sqlext.Executor
}

var (
	// prove documented interface implementations
	_ Interface = &gsql.DB{}
	_ Interface = &gsql.Tx{}
)

var olapSemaphore = syncext.NewSemaphore(2)

// RunOLAPQueries executes a DB transaction with increased `work_mem` setting.
// As the name implies, this is useful for OLAP queries that perform expensive
// joins and aggregations in a way that benefits from having more RAM available
// than the default.
//
// This should only be used sparingly; each process is only allowed to run two
// such queries at the same time to limit the total memory usage on the DB server.
func RunOLAPQueries(db *gsql.DB, action func(tx *gsql.Tx) error) error {
	return olapSemaphore.RunFallible(func() error {
		// since we don't have direct control over the connections which live in
		// database/sql.Conn's connection pool, we can only limit the effect of the
		// `SET work_mem TO ...` statement to the intended action by wrapping it in a
		// transaction
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer sqlext.RollbackUnlessCommitted(tx)

		// the SET statement does not accept a placeholder for its argument, so we
		// need to do the ugly thing and escape by hand
		workMemStr := osext.GetenvOrDefault("LIMES_DB_WORKMEM_FOR_OLAP", "128MB")
		_, err = tx.Exec(fmt.Sprintf(`SET LOCAL work_mem TO '%s'`, strings.ReplaceAll(workMemStr, "'", "''")))
		if err != nil {
			return fmt.Errorf("could not set work_mem = %q for OLAP query: %w", workMemStr, err)
		}

		err = action(tx)
		if err != nil {
			return err
		}

		return tx.Rollback()
	})
}
