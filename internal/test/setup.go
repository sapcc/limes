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
	"strings"
	"testing"

	"github.com/go-gorp/gorp/v3"
	"github.com/gophercloud/gophercloud"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/osext"
	"gopkg.in/yaml.v2"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
	_ "github.com/sapcc/limes/internal/test/plugins"
)

type setupParams struct {
	DBFixtureFile string
	ConfigYAML    string
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

// WithConfig is a SetupOption that initializes the test cluster from a
// configuration provided as YAML. This option is effectively required, as an
// empty cluster configuration is not allowed.
func WithConfig(yamlStr string) SetupOption {
	return func(params *setupParams) {
		params.ConfigYAML = normalizeInlineYAML(yamlStr)
	}
}

func normalizeInlineYAML(yamlStr string) string {
	//In the source code, we usually use tabs for YAML indentation because the
	//code is indented with tabs, and mixed indentation confuses some editors.
	//But YAML insists on using spaces for indentation.
	return strings.Replace(yamlStr, "\t", "  ", -1)
}

// Setup contains all the pieces that are needed for most tests.
type Setup struct {
	//fields that are always set
	DB      *gorp.DbMap
	Cluster *core.Cluster
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
	s.Cluster = initCluster(t, params.ConfigYAML)
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

func initCluster(t *testing.T, configYAML string) *core.Cluster {
	var cfg core.ClusterConfiguration
	err := yaml.UnmarshalStrict([]byte(configYAML), &cfg)
	if err != nil {
		t.Fatal(err)
	}

	cluster, errs := core.NewCluster(cfg)
	if errs.IsEmpty() {
		errs = cluster.Connect(nil, gophercloud.EndpointOpts{})
	}
	for _, err := range errs {
		t.Error(err)
	}
	if t.Failed() {
		t.FailNow()
	}
	return cluster
}

// AddCapacityPlugin extends the Setup with a new CapacityPlugin in the middle
// of a testcase. The `configYAML` must contain a CapacitorConfiguration.
//
// TODO Try to remove this once unit tests have been sufficiently modularized
// to not require this kind of messing with the setup during the test.
// TODO Alternatively, reduce code duplication with core.NewCluster().
func (s *Setup) AddCapacityPlugin(t *testing.T, configYAML string) core.CapacityPlugin {
	t.Helper()

	var cfg core.CapacitorConfiguration
	err := yaml.UnmarshalStrict([]byte(normalizeInlineYAML(configYAML)), &cfg)
	if err != nil {
		t.Fatal(err)
	}

	plugin := core.CapacityPluginRegistry.Instantiate(cfg.PluginType)
	if plugin == nil {
		t.Fatal("no such capacity plugin: " + cfg.PluginType)
	}

	err = yaml.UnmarshalStrict([]byte(cfg.Parameters), plugin)
	if err != nil {
		t.Fatal("failed to supply params to capacitor: " + err.Error())
	}
	err = plugin.Init(nil, gophercloud.EndpointOpts{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Cluster.CapacityPlugins[cfg.ID] = plugin
	return plugin
}
