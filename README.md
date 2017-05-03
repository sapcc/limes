# Limes

[![Build Status](https://travis-ci.org/sapcc/limes.svg?branch=master)](https://travis-ci.org/sapcc/limes)
[![Coverage Status](https://coveralls.io/repos/github/sapcc/limes/badge.svg?branch=master)](https://coveralls.io/github/sapcc/limes?branch=master)
[![Go Report Card](https://goreportcard.com/badge/github.com/sapcc/limes)](https://goreportcard.com/report/github.com/sapcc/limes)
[![GoDoc](https://godoc.org/github.com/sapcc/limes?status.svg)](https://godoc.org/github.com/sapcc/limes)

Limes is an OpenStack-compatible quota/usage tracking service, originally designed for SAP's internal cloud.

Pronounce the name like the [Ancient Roman border wall][wp-limes], not like the fruit.

# The idea: Hierarchical quota delegation

OpenStack groups access into three levels:

1. the cluster (the sum of all the resources in an OpenStack installation, e.&nbsp;g.&nbsp;hypervisors or storage capacity)
2. Keystone domains within that cluster
3. Keystone projects within each domain

Limes enables a similar hierarchy for resource usage and quotas: After having reviewed the cluster's capacity, a cluster
admin can allocate quotas to domains. The domain admin can then sublease that quota to its projects. Limes will then
write these approved project quotas into the backend services that actually manage the resources. Limes also tracks
resource usage in all projects in all domains, so that users can make informed decisions about resource allocation at
all levels of the hierarchy.

# Documentation

## For users

* [Index](./docs/users/index.md)
* [API specification](./docs/users/api-v1-specification.md)
* [API usage example](./docs/users/api-example.md)

## For developers

* [Developer's guide](./docs/developers/guide.md)
* [Code structure overview](./docs/developers/code-overview.md)

# Installation

There's a Makefile, so do:

* `make` to just compile and run the binaries from the `build/` directory
* `make && make install` to install to `/usr`
* `make && make install PREFIX=/some/path` to install to `/some/path`
* `make docker` to build the Docker image (set image name and tag with the `DOCKER_IMAGE` and `DOCKER_TAG` variables)

## Usage

Prerequisites: Create a PostgreSQL database for Limes, and a service user in at least one OpenStack installation.

1. Write a configuration file for your environment, by following the [example configuration][ex-conf].

2. Populate the DB schema by running `limes migrate config.yaml`. For more fine-grained control of migrations (e.g.
   rollback), download the [`migrate` tool][migrate] and follow the instructions over there.

3. For each cluster, start `limes serve config.yaml $cluster_id` and `limes collect config.yaml $cluster_id`.

4. For each cluster, register the public URL of the `limes serve` process in the Keystone service catalog with service type `resources`.

A lot of that is automated by our team's [Helm chart for Limes][chart].

[wp-limes]: https://en.wikipedia.org/wiki/Limes
[ex-conf]:  ./docs/example-config.yaml
[migrate]:  https://github.com/mattes/migrate
[chart]:    https://github.com/sapcc/helm-charts/tree/master/openstack/limes
