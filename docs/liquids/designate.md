<!--
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

# Liquid: `designate`

This liquid provides support for the DNS service Designate.

- The suggested service type is `liquid-designate`.
- The suggested area is `network`.

## Service-specific configuration

| Field | Type | Description |
| ----- | ---- | ----------- |
| `prometheus_config.api` | [`promquery.Config`](https://pkg.go.dev/github.com/sapcc/go-bits/promquery#Config) | Configuration for the Prometheus connection from which usage data is queried by the liquid. |
| `prometheus_config.queries.zones` | [`text/template`](https://pkg.go.dev/text/template) compatible string | Prometheus query for scraping the number of zones per project. The template should contain a filter string `{{.ProjectUUID}}` to be filled with the UUID of the project to be queried for usages. |
| `prometheus_config.queries.recordsets_per_zone` | [`text/template`](https://pkg.go.dev/text/template) compatible string | Prometheus query for scraping the maximum number of recordsets across all zones of this project. The template should contain a filter string `{{.ProjectUUID}}` to be filled with the UUID of the project to be queried for usages. |

## Resources

| Resource               | Unit | Capabilities                         |
| ---------------------- | ---- | ------------------------------------ |
| `zones`                | None | HasCapacity = false, HasQuota = true |
| `recordsets_per_zone`  | None | HasCapacity = false, HasQuota = true |

When the `recordsets_per_zone` quota is set, the backend quota for records per zone is set to 20 times that value, to
fit into the `records_per_recordset` quota (which is set to 20 by default in Designate). The quota for records per zone
cannot be managed explicitly in this liquid.

### Considerations for cloud operators

Because querying usage for the zones and especially recordsets resources is very inefficient using the Designate API, this liquid will instead collect usage data from Prometheus metrics.
Your Designate operator will have to provide suitable metrics that report the count of all zones per project, as well as the number of recordsets in those zones.
Be aware that when exporting these figures from the designate database, you have to take into account that deleted zones are soft deleted at first and have to be filtered from the result (`status != "DELETED"`).
