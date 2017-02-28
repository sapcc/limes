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

package main

import (
	"fmt"
	"os"

	_ "github.com/mattes/migrate/driver/postgres"
	"github.com/mattes/migrate/migrate"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/spf13/pflag"
)

var databaseURL = pflag.StringP("database-url", "d", "",
	"database URL (required, format: \"postgres://user:pass@host:port/database\")")
var migrationsPath = pflag.StringP("migrations-path", "m", "/usr/share/limes/migrations",
	"path to directory containing migration files (optional, default: \"/usr/share/limes/migrations\")")

func main() {
	pflag.Parse()
	if *databaseURL == "" {
		limes.Log(limes.LogFatal, "missing --database-url argument")
	}

	errs, ok := migrate.UpSync(*databaseURL, *migrationsPath)
	if !ok {
		limes.Log(limes.LogError, "ERROR: migration failed, see errors on stderr")
		for _, err := range errs {
			fmt.Fprintln(os.Stderr, err.Error())
		}
	}

}
