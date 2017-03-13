# Limes

[![Build Status](https://travis-ci.org/sapcc/limes.svg?branch=master)](https://travis-ci.org/sapcc/limes)
[![Coverage Status](https://coveralls.io/repos/github/sapcc/limes/badge.svg?branch=master)](https://coveralls.io/github/sapcc/limes?branch=master)

Limes is an OpenStack-compatible quota/usage tracking service, originally designed for SAP's internal cloud.

Pronounce the name like the [Ancient Roman border wall][wp-limes], not like the fruit.

**WARNING:** This is still in pre-alpha stage. Most notably, the API component is completely missing, and there are no tests yet.

# Installation

There's a Makefile, so do:

* `make` to just compile and run the binaries from the `build/` directory
* `make && make install` to install to `/usr`
* `make && make install PREFIX=/some/path` to install to `/some/path`
* `make docker` to build the Docker image (set image name and tag with the `DOCKER_IMAGE` and `DOCKER_TAG` variables)

## Usage

Prerequisites: Create a PostgreSQL database for Limes, and a service user in at least one OpenStack installation.

1. Write a configuration file for your environment, by following the [example configuration][ex-conf]

2. Populate the DB schema by running `limes-migrate config.yaml`. For more fine-grained control of migrations (e.g.
   rollback), download the [`migrate` tool][migrate] and follow the instructions over there.

3. For each cluster, start `limes-api config.yaml $cluster_id` and `limes-collect config.yaml $cluster_id`.

[wp-limes]: https://en.wikipedia.org/wiki/Limes
[ex-conf]:  ./docs/example-config.yaml
[migrate]:  https://github.com/mattes/migrate
