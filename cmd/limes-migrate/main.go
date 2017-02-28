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
