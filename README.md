# Limes

Limes is an OpenStack-compatible quota/usage tracking service, originally designed for SAP's internal cloud.

Pronounce the name like the [Ancient Roman border wall][wp-limes], not like the fruit.

# Installation

There's a Makefile, so do:

* `make` to just compile and run the binaries from the `build/` directory
* `make && make install` to install to `/usr`
* `make && make install PREFIX=/some/path` to install to `/some/path`

## Usage

Prerequisites: Create a PostgreSQL database for Limes, and a service user in at least one OpenStack installation.

1. Write a configuration file for your environment, by following the [example configuration][ex-conf]

2. Populate the DB schema by running `limes-migrate config.yaml`. For more fine-grained control of migrations (e.g.
   rollback), download the [`migrate` tool][migrate] and follow the instructions over there.

[wp-limes]: https://en.wikipedia.org/wiki/Limes
[ex-conf]:  ./docs/example-config.yaml
[migrate]:  https://github.com/mattes/migrate
