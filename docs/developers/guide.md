# Developer guide

This guide contains everything that you need to get a local installation of Limes up and running for development purposes.

### Prerequisite: PostgreSQL

Make sure that Postgres is installed and running. If not yet installed, consult your package manager of choice. It will
usually tell you what to do at installation time, or somewhere in the distro's documentation. You will need to do at
least `initdb`. Sometimes additional foo is needed to create a user account. Most of the time, the user `postgres` is
auto-created and can connect to the database from localhost without password. To check that everything works, run

```bash
$ psql -U postgres
```

This should show you a prompt.

**Hint:** For macOS, I've heard that [Postgres.app][pg-app] offers a nice out-of-the-box experience.

### Prerequisite: Go

Make sure that Go is installed. A fairly recent one is required: If your package manager does not offer 1.8 or newer,
grab the official binaries from golang.org. Add the following to your shell rc:

```bash
export GOPATH=$HOME/go
export GOBIN=$GOPATH/bin
export PATH=$PATH:$GOBIN
```

`$GOPATH/src` is where Go will put all your Go source code. You can change the GOPATH to whatever you like, but stick
with it once set.

In addition to the builtin checkers and testsuite runners in the `go` command, Limes runs checks from `golint`, so
install that with

```bash
$ go get -u github.com/golang/lint/golint
```

### Building

Checkout the Limes repository into your GOPATH:

```bash
$ go get github.com/sapcc/limes
$ cd $GOPATH/src/github.com/sapcc/limes
$ make
$ make check
```

The `make check` will spew out a bit of bizarre-looking log, but you can easily see if it went well by looking for the
friendly green "All tests successful" message at the end.

**Note:** On macOS, `echo(1)` is too stupid to understand color codes. You should still see the "All tests successful"
message in the last line, but minus color and plus mojibake.

If `make check` complains about a missing libsqlite3.so, you will need to install SQLite3 via your usual package
manager. It's probably there already, though.

### Configuring

If you already have a deployment of Limes, you can quickly get a nearly sufficient configuration file by just
downloading it from there. For example, if you use the [official Helm chart for Limes][chart], here's what you would do:

```bash
$ kubectl config use-context staging
$ kubectl --namespace limes get pods -l app=limes-collect
# select one of the pods shown
$ kubectl --namespace limes exec "${POD_NAME}" -- cat /etc/limes/limes.yaml > test.yaml
```

You will need to edit the `database` section to point to your local database and to the migration files in the repo:

```yaml
database:
  location: "postgres://postgres@localhost/limes?sslmode=disable"
  migrations: /my/gopath/src/github.com/sapcc/limes/pkg/db/migrations
```

Adjust the database user name if required, and replace `/my/gopath` by your GOPATH. Furthermore, edit the `api` section to point it to a valid `policy.json` file. The example from the `docs` directory should work fine:

```yaml
api:
  ...
  policy: /my/gopath/src/github.com/sapcc/limes/docs/example-policy.json
```

Of course, you can also use the same trick as before and download the policy file from the existing deployment.

Finally, to avoid confusion, you need to set the `collector.auto_align_quotas` flag to `false` when operating against an
OpenStack cluster that already has a Limes instance.

### Running

Before any other Limes job can run, you must create the database and populate the schema:

```bash
$ ./build/limes migrate test.yaml
```

For some reason, that sometimes fails with `database does not exist` even though it should autocreate the database. If that happens, create the database manually before trying again:

```bash
psql -U postgres -c 'CREATE DATABASE limes;'
```

When `limes migrate` has completed successfully, you can run any other Limes job.

* the API: `./build/limes serve test.yaml ccloud`
* the collectors: `./build/limes collect test.yaml ccloud`

There are two further subcommands that assist with the development of new plugins: `limes test-scrape` invokes all
enabled quota plugins on a single project, and dumps the quota/usage data that was scraped by the plugins. `limes
test-scan-capacity` invokes all enabled capacity plugins on the current cluster, and dumps the capacity data that was
scrapes by the plugins.

The following environment variables can be useful during development:

```bash
export LIMES_DEBUG=1       # show debug logs
export LIMES_DEBUG_SQL=1   # log executed SQL statements
export LIMES_INSECURE=1    # disable SSL certificate verification (useful with mitmproxy)
```

To execute arbitrary SQL on the database, use psql:

```bash
$ psql -U postgres -d limes
```

[pg-app]:   http://postgresapp.com/
[chart]:    https://github.com/sapcc/helm-charts/tree/master/openstack/limes
