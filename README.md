# Limes

[![Build Status](https://travis-ci.org/sapcc/limes.svg?branch=master)](https://travis-ci.org/sapcc/limes)
[![Coverage Status](https://coveralls.io/repos/github/sapcc/limes/badge.svg?branch=master)](https://coveralls.io/github/sapcc/limes?branch=master)
[![Go Report Card](https://goreportcard.com/badge/github.com/sapcc/limes)](https://goreportcard.com/report/github.com/sapcc/limes)
[![GoDoc](https://godoc.org/github.com/sapcc/limes?status.svg)](https://godoc.org/github.com/sapcc/limes)

Limes is an OpenStack-compatible quota/usage tracking service, originally designed for SAP's internal cloud.

Pronounce the name like the [Ancient Roman border wall][wp-limes], not like the fruit. (Mnemonic: The original Limes was installed when the Romans wanted to put a quota on Germanic land use.)

# The idea: Hierarchical quota delegation

OpenStack groups access into three levels:

1. the cluster (the sum of all the resources in an OpenStack installation, e.g. hypervisors or storage capacity)
2. Keystone domains within that cluster
3. Keystone projects within each domain

Limes enables a similar hierarchy for resource usage and quotas: After having reviewed the cluster's capacity, a cluster
admin can allocate quotas to domains. The domain admin can then sublease that quota to its projects. Limes will then
write these approved project quotas into the backend services that actually manage the resources. Limes also tracks
resource usage in all projects in all domains, so that users can make informed decisions about resource allocation at
all levels of the hierarchy.

## Unique features

* Limes can take over the handling of initial project quotas: All quotas for a new project (or domain) will be set to zero initially, until a sufficiently privileged user approves quota explicitly.
* As a unique feature, Limes can also track physical resources that are shared between multiple OpenStack clusters.
* Limes records quota changes in an Open Standards [CADF Format](https://www.dmtf.org/sites/default/files/standards/documents/DSP0262_1.0.0.pdf), and is compatible with other cloud based audit APIs (e.g. [Hermes](https://github.com/sapcc/hermes)).
* Quota and usage data can be exposed as [Prometheus metrics](https://prometheus.io) for monitoring and alerting.

# Documentation

## For users

* [Index](./docs/users/index.md)
* [CLI client](https://github.com/sapcc/limesctl)
* [API specification](./docs/users/api-v1-specification.md)
* [API usage example](./docs/users/api-example.md)
* [Audit trail](./docs/users/audit.md)

## For operators

* [Overview and installation instructions](./docs/operators/index.md)
* [Configuration options](./docs/operators/config.md)
* [List of metrics](./docs/operators/metrics.md)

## For developers

* [Developer's guide](./docs/developers/guide.md)
* [Code structure overview](./docs/developers/code-overview.md)

[wp-limes]: https://en.wikipedia.org/wiki/Limes
