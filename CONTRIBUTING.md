<!--
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

# Overview for developers/contributors

This guide describes Limes' code structure and contains everything that you need to get a local installation of Limes up and running for development purposes.

## Prerequisites

- [PostgreSQL](https://www.postgresql.org/)
  - The minimum is to have the server binaries installed.
    The `pg_ctl` command needs to be visible in `$PATH`; you might have to add an entry like `/usr/lib/postgresql/$VERSION/bin`.
    You don't need a server running; the test machinery will start a local server process on its own when needed.
    We also recommend installing the client binaries, like `psql` or `pg_dump`, to aid with debugging of failed tests.
- [Go](https://go.dev/)
  - If going through your package manager, check that it does not have an ancient Go version (_\*cough\*_ Debian _\*cough\*_).
    You should have at least the Go version required in the second paragraph of the `go.mod` file in this repository.
  - If your package manager has both `go` and `go-tools`, install both.
    `go-tools` is technically not required, but many editors support running the code formatter contained therein (`goimports`) automatically if it is in `$PATH`.
  - Make sure that the output of `go env GOBIN` is a directory that is in your `$PATH`.
    If `go env GOBIN` is empty, then `$(go env GOPATH)/bin` should be in your `$PATH`.

Alternatively, if you have [Nix](https://nixos.org/) installed, you can run `nix-shell` to get all that in a single command.

## Testing methodology

The vast majority of Limes code is covered by automated tests that can be executed with `make check`.
On success, this will produce a coverage report at `build/cover.html`.
The test suite features a self-contained testing database with files being stored in `.testdb`.
The `.testdb` directory contains debugging scripts for interacting with the database contents, in order to inspect the database state after failed tests.

When only changing this code, you can stop reading here; you know all that you need to know.

The only significant exception is the liquids: adapters that translate between an OpenStack service's native API
and the [LIQUID API](https://pkg.go.dev/github.com/sapcc/go-api-declarations/liquid) that Limes understands
(LIQUID = Limes Interface for Quota and Usage Interrogation and Discovery).

We don't do automated testing of liquids because, in our experience, it's not very useful:
- Either tests would require an OpenStack cluster to be present.
  This makes them less versatile, e.g. they cannot be executed in GitHub Actions.
- Or alternatively, tests would try to mock away the OpenStack service (e.g. using recorded HTTP responses).
  This makes them more brittle, e.g. most relevant changes to the code would require changes to the tests.

In practice, ongoing test coverage for the liquids is provided by our QA deployments.
This is especially useful in case of OpenStack upgrades that cause backwards incompatibilities.
That's something that a test against a mock filled with recorded responses would not be able to find.

When changing code in a liquid, we expect you to conduct manual tests against an OpenStack cluster, using the test tooling explained below.
For changes to read-only operations (BuildServiceInfo, ReportCapacity, ReportUsage), it is absolutely encouraged to test against productive deployments, too.
Real customers are just much better at coming up with interesting edge cases than anything in a QA deployment. :)

## Running a liquid locally for manual testing

You will need to have OpenStack credentials, usually ones that give you cloud-admin access to the respective service.
See [the documentation of the NewProviderClient function](https://pkg.go.dev/github.com/sapcc/go-bits/gophercloudext#NewProviderClient) for which variables are allowed.

In the repo root directory, create the following minimal policy file, replacing the `role:` names as appropriate for your deployment:

```shellSession
$ cat test-policy.json
{
  "readwrite": "role:cloud_resource_admin",
  "readonly": "role:cloud_resource_viewer or rule:readwrite",
  "liquid:get_info": "rule:readonly",
  "liquid:get_capacity": "rule:readonly",
  "liquid:get_usage": "rule:readonly",
  "liquid:set_quota": "rule:readwrite",
  "liquid:change_commitments": "rule:readwrite"
}
```

Furthermore, if the liquid you want to run takes configuration (that is, if `opts.TakesConfiguration = true` is set for it in `main.go`),
write a configuration file according to the [liquid's documentation in `docs/liquids/`](docs/liquids/index.md).

Finally, run the liquid like this, replacing `foobar` by the actual liquid name:

```sh
export LIMES_DEBUG=true # show debug logs (enable only if needed, debug logs might be very verbose)
export LIQUID_LISTEN_ADDRESS=:8080
export LIQUID_POLICY_PATH=$PWD/test-policy.json
export LIQUID_CONFIG_PATH=$PWD/config-liquid-foobar.json # if opts.TakesConfiguration == true
make && ./build/limes liquid foobar
```

**TODO:** It would be nice to have a simplified invocation for this, e.g. `limes test-liquid foobar --port 8080 --config $CONFIG_PATH`.

## Querying a liquid running locally

Just having the liquid running does not do a lot.
It's a service exposing a REST API, so you need to query it.

Because doing so with curl is tedious, we have tooling for it in [limesctl](https://github.com/sapcc/limesctl).
Install `limesctl` and take a look at `limesctl liquid --help` for which subcommands are available.
Note that you need to run `limesctl` in a second terminal while the liquid itself keeps running in the first one.

For example, this command queries the liquid started above for a capacity report:

```sh
limesctl liquid report-capacity foobar --endpoint http://localhost:8080/
```

It's also highly recommended to use the `--compare` flag to have limesctl run the same query both against the deployed liquid and your local copy, and report the diff.
When refactoring, you want to check that the diff is empty.
When adding new logic, you want to check that the diff matches what you expect.

## Testing calls against Keystone

Keystone integration is not mediated via LIQUID and goes through the `core.DiscoveryPlugin` interface instead.
To test this interface's method calls, run one of

```sh
make && ./build/limes test-list-domains
make && ./build/limes test-list-projects <domain-name-or-uuid>
```

with `OS_*` variables containing OpenStack credentials that allow access to the respective Keystone API calls.

## Code structure

Once compiled, Limes is only a single binary containing subcommands for the various components (`limes serve`, `limes collect` and `limes serve-data-metrics`).
This reduces the size of the compiled application dramatically since a lot of code is shared.
The main entrypoint is in `main.go`, from which everything else follows.
The bulk of the source code is below `internal/`, organized into packages as follows (listed from the bottom up):

| Package | `go test` | Contents |
| --- | :---: | --- |
| `internal/util` | yes | various small utility functions (esp. for type conversion), custom data types that can be serialized into the DB and/or API |
| `internal/db` | no | database connection handling, schema definitions, ORM model classes, utility functions |
| `internal/core` | yes | core interfaces (DiscoveryPlugin, QuotaPlugin, CapacityPlugin) and data structures (Configuration, Cluster), config parsing and validation |
| `internal/test` | no | testing helpers: mock implementations of core interfaces, test runners, etc. |
| `internal/datamodel` | yes | higher-level functions that operate on the ORM model classes (not in `internal/db` because of dependency on stuff from `internal/limes`) |
| `internal/collector` | yes | functionality of `limes collect` and `limes serve-data-metrics` |
| `internal/reports` | no | helper for `internal/api`: rendering of reports for GET requests |
| `internal/api` | yes | functionality of `limes serve` |
| `internal/liquids` | no | implementations of liquid adapters (these only depend on [go-api-declarations](https://github.com/sapcc/go-api-declarations) and [go-bits](https://github.com/sapcc/go-bits); not on any other Limes internals) |

The database is defined by SQL files in `internal/db/migrations.go`.
The filenames follow the requirements of [the library that Limes uses for handling the DB schema](https://github.com/golang-migrate/migrate).
