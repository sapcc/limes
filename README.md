<!--
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

# Limes

[![CI](https://github.com/sapcc/limes/actions/workflows/ci.yaml/badge.svg)](https://github.com/sapcc/limes/actions/workflows/ci.yaml)
[![Coverage Status](https://coveralls.io/repos/github/sapcc/limes/badge.svg?branch=master)](https://coveralls.io/github/sapcc/limes?branch=master)
[![Go Report Card](https://goreportcard.com/badge/github.com/sapcc/limes)](https://goreportcard.com/report/github.com/sapcc/limes)
[![GoDoc](https://godoc.org/github.com/sapcc/limes?status.svg)](https://godoc.org/github.com/sapcc/limes)

Limes is an OpenStack-compatible quota/usage tracking service, originally designed for SAP's internal cloud.

Pronounce the name like the [Ancient Roman border wall][wp-limes], not like the fruit. (Mnemonic: The original Limes was installed when the Romans wanted to put a quota on Germanic land use.)

# The idea: Automatic quota distribution

Limes can discover capacity and usage for various types of OpenStack resources.
It can then be set up to distribute quota automatically among all projects in a dynamic and automated fashion.
Both cloud admins and project admins have several knobs to control their quota assignments in a controlled fashion.

## Unique features

* Limes records quota changes in the Open Standards [CADF Format](https://www.dmtf.org/sites/default/files/standards/documents/DSP0262_1.0.0.pdf), and is compatible with other cloud based audit APIs (e.g. [Hermes](https://github.com/sapcc/hermes)).
* Quota and usage data can be exposed as [Prometheus metrics](https://prometheus.io) for monitoring and alerting.

# Documentation

## For users

* [Index](./docs/users/index.md)
* [CLI](https://github.com/sapcc/limesctl)
* [API specification](./docs/users/api-v1-specification.md)
* [API usage example](./docs/users/api-example.md)
* [Audit trail](./docs/users/audit.md)

## For operators

* [Overview and installation instructions](./docs/operators/index.md)
* [Configuration options](./docs/operators/config.md)
* [List of metrics](./docs/operators/metrics.md)

## For developers

* [Developer's guide](./CONTRIBUTING.md)

[wp-limes]: https://en.wikipedia.org/wiki/Limes
