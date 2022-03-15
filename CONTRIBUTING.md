# Overview for developers/contributors

This guide describes Limes' code structure and contains everything that you need to get a local installation of Limes up and running for development purposes.

## Prerequisites

* Postgres
* Go

## Testing methodology

### Run Limes locally

Before you can run Limes, you need to set up some [configuration
options](../operators/config.md#configuration-options) via environment variables and
create a [config file](../operators/config.md#configuration-file) for cluster options.

#### Configuration Options

Limes already has good defaults for its configuration so you won't need to do a lot of
configuring. Here is an example of some options that you might want to configure:

```bash
export LIMES_API_POLICY_PATH=$(pwd)/docs/example-policy.yaml
export LIMES_DB_CONNECTION_OPTIONS='sslmode=disable'
export LIMES_DB_USERNAME=$USER # on macOS
```

**Hint**: for convenience, you can store the above commands in a `.env` file and execute
`source .env` once to set up the configuration options for your current shell session.

#### Configuration File

Refer to the [config guide](../operators/config.md#configuration-file) for instructions
relating to the configuration file.

If you already have a deployment of Limes, you can quickly get a nearly sufficient configuration file by just
downloading it from there. For example, if you use the [official Helm chart for Limes][chart], here's what you would do:

```bash
$ kubectl config use-context qa-de-1
$ kubectl --namespace limes get pods -l app=limes-collect
# select one of the pods shown
$ kubectl --namespace limes exec "${POD_NAME}" -- cat /etc/limes/limes.yaml > test.yaml
```

To avoid confusion, you need to set the `authoritative` field to `false` when operating against an
OpenStack cluster that already has a Limes instance.

#### Run

Make sure that Postgres is running then you can now run both Limes jobs:

* the API: `./build/limes serve test.yaml`
* the collector: `./build/limes collect test.yaml`

### Run the test suite

Tests can be run with the helper script `testing/with-postgres-db.sh` to use a
self-contained testing database located at `testing/postgresql*`.

Run the full test suite with:

```bash
./testing/with-postgres-db.sh make check
```

This will produce a coverage report at `build/cover.html`.

### Test Harnesses

The following subcommands assist with the development of new plugins:

* `limes test-get-quota` invokes the specific quota plugin for a specific service type on
  a single project, and dumps the quota/usage data that was scraped by the plugin.
* `limes test-get-rates` invokes the specific quota plugin for a specific service type on
  a single project, and dumps the rate limits data that was scraped by the plugin.
* `limes test-set-quota` invokes the specific quota plugin for a specific service type on
  a single project, and can be used to test setting a new quota value for a resource.
* `limes test-scan-capacity` invokes all enabled capacity plugins on the current cluster
  and dumps the capacity data that was scrapes by the plugins.

Run `limes --help` for details about the arguments that are required for the subcommands.

Additionally, the following environment variables can be useful during development:

```bash
export LIMES_DEBUG=1       # show debug logs
export LIMES_INSECURE=1    # disable SSL certificate verification (useful with mitmproxy)
```

## Code structure

Once compiled, Limes is only a single binary containing subcommands for the various components (`limes serve` and `limes
collect`). This reduces the size of the compiled application dramatically since a lot of code is shared. The main
entrypoint is in `cmd/limes/main.go`, from which everything else follows.

The `main.go` is fairly compact. The main source code is below `pkg/`, organized into packages as follows: (listed
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

Only the top-level package is considered public API. Third-party programs should not import anything from `pkg/` and its subdirectories..

The database is defined by SQL files in `pkg/db/migrations.go`. The contents follow the PostgreSQL dialect of SQL, the
filenames follow the requirements of [the library that Limes uses for handling the DB schema][migrate].

[yaml]: http://yaml.org/
[chart]: https://github.com/sapcc/helm-charts/tree/master/openstack/limes
[migrate]: https://github.com/golang-migrate/migrate
