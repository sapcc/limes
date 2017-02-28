# Limes

Limes is an OpenStack-compatible quota/usage tracking service, originally designed for SAP's internal cloud.

# Installation

There's a Makefile, so do:

* `make` to just compile
* `make && make install` to install to `/usr`
* `make && make install PREFIX=/some/path` to install to `/some/path`

## Setting up the database

You will need a running PostgreSQL. Create a database for Limes, then populate the DB schema by running:

``` bash
# if you did not "make install"
$ ./build/limes-migrate -m ./pkg/db/migrations -d 'postgres://user:pass@host:port/database'

# if you did "make install"
$ /usr/bin/limes-migrate -d 'postgres://user:pass@host:port/database'

# if you did "make install" with a custom PREFIX
$ ${PREFIX}/bin/limes-migrate -m ${PREFIX}/share/limes/migrations -d 'postgres://user:pass@host:port/database'
```

For more fine-grained control (e.g. rollback of migrations), download the
[`migrate` tool](https://github.com/mattes/migrate) and follow the instructions over there.
