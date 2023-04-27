/******************************************************************************
*
*  Copyright 2017-2023 SAP SE
*
*  Licensed under the Apache License, Version 2.0 (the "License");
*  you may not use this file except in compliance with the License.
*  You may obtain a copy of the License at
*
*      http://www.apache.org/licenses/LICENSE-2.0
*
*  Unless required by applicable law or agreed to in writing, software
*  distributed under the License is distributed on an "AS IS" BASIS,
*  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
*  See the License for the specific language governing permissions and
*  limitations under the License.
*
******************************************************************************/

package test

import (
	"net/url"
	"testing"

	"github.com/go-gorp/gorp/v3"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/osext"

	"github.com/sapcc/limes/internal/db"
)

type setupParams struct {
	DBFixtureFile string
}

// SetupOption is an option that can be given to NewSetup().
type SetupOption func(*setupParams)

// WithDBFixtureFile is a SetupOption that prefills the test DB by executing
// the SQL statements in the given file.
func WithDBFixtureFile(file string) SetupOption {
	return func(params *setupParams) {
		params.DBFixtureFile = file
	}
}

// Setup contains all the pieces that are needed for most tests.
type Setup struct {
	//fields that are always set
	DB *gorp.DbMap
}

// NewSetup prepares most or all pieces of Keppel for a test.
func NewSetup(t *testing.T, opts ...SetupOption) Setup {
	logg.ShowDebug = osext.GetenvBool("LIMES_DEBUG")
	var params setupParams
	for _, option := range opts {
		option(&params)
	}

	var s Setup
	s.DB = initDatabase(t, params.DBFixtureFile)
	return s
}

func initDatabase(t *testing.T, fixtureFile string) *gorp.DbMap {
	//nolint:errcheck
	postgresURL, _ := url.Parse("postgres://postgres:postgres@localhost:54321/limes?sslmode=disable")
	dbm, err := db.InitFromURL(postgresURL)
	if err != nil {
		t.Error(err)
		t.Log("Try prepending ./testing/with-postgres-db.sh to your command.")
		t.FailNow()
	}

	//reset the DB contents and populate with initial resources if requested
	easypg.ClearTables(t, dbm.Db, "cluster_capacitors", "cluster_services", "domains") //all other tables via "ON DELETE CASCADE"
	if fixtureFile != "" {
		easypg.ExecSQLFile(t, dbm.Db, fixtureFile)
	}
	easypg.ResetPrimaryKeys(t, dbm.Db, "cluster_services", "domains", "domain_services", "projects", "project_services")

	return dbm
}
