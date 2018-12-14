# Code structure overview

Once compiled, Limes is only a single binary containing subcommands for the various components (`limes serve` and `limes
collect`). This reduces the size of the compiled application dramatically since a lot of code is shared. The main
entrypoint is in `cmd/limes/main.go`, from which everything else follows.

The `main.go` is fairly compact. The main sourcecode is below `pkg/`, organized into packages as follows: (listed
from the bottom up)

| Package | `go test` | Contents |
| --- | :---: | --- |
| _(toplevel)_ | yes | types for data structures that appear in the Limes API |
| `pkg/util` | no | various small utility functions (esp. for type conversion) |
| `pkg/db` | no | database configuration, connection handling, ORM model classes, utility functions |
| `pkg/core` | yes | core interfaces (DiscoveryPlugin, QuotaPlugin, CapacityPlugin) and data structures (Configuration, Cluster), config parsing and validation |
| `pkg/test` | no | testing helpers: mock implementations of core interfaces, test runners, etc. |
| `pkg/plugins` | no | implementations of QuotaPlugin and CapacityPlugin |
| `pkg/datamodel` | no | higher-level functions that operate on the ORM model classes (not in `pkg/db` because of dependency on stuff from `pkg/limes` |
| `pkg/collector` | yes | functionality of `limes collect` |
| `pkg/reports` | no | helper for `pkg/api`: rendering of reports for GET requests |
| `pkg/api` | yes | functionality of `limes serve` |

Only the toplevel package is considered public API. Code that is not in this repository should not import anything from below `pkg/`.

The database is defined by SQL files in `pkg/db/migrations.go`. The contents follow the PostgreSQL dialect of SQL, the
filenames follow the requirements of [the library that Limes uses for handling the DB schema][migrate].

## Testing methodology

Most of the tests are in the top-level packages `pkg/collector` and `pkg/api`. I consider this enough because everything
else is used by these packages, except for the plugin implementations in `pkg/plugins`. We do not test these yet because
`go test` cannot assume the presence of an OpenStack cluster anywhere near where the test runs.

During `go test`, Postgres is substituted for SQLite. The `pkg/test` module provides mock implementations of
`limes.Driver`, `limes.QuotaPlugin`, `limes.CapacityPlugin`, `limes.DiscoveryPlugin` and `time.Now`, and a few helper
functions to load and assert SQL data as well as simulate HTTP requests.

[migrate]: https://github.com/golang-migrate/migrate
