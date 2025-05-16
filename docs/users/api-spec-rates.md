<!--
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

# Limes Rate API specification

The URLs indicated in the headers of each section are relative to the endpoint URL advertised in the Keystone
catalog under the service type `limes-rates`.

Where permission requirements are indicated, they refer to the default policy. Limes operators can configure their
policy differently, so that certain requests may require other roles or token scopes.

Use the table of contents icon
<img src="https://github.com/github/docs/raw/main/contributing/images/table-of-contents.png" width="25" height="25" />
near the top left corner of this document to jump to a specific section on this page.

## Concepts

The Limes Rate API deals with **rates** that each describe countable actions of a certain kind. Rates are grouped into
the **services** that manage these actions. Services are identified by a type string, which is usually identical to the
type of the respective service entry in the Keystone service catalog. Limes further groups services into **areas**: For
example, the services for block storage, shared filesystem storage and object storage are grouped into the "storage"
area.

Most rates are **counted**, i.e. they count discrete actions. Rates can also be **measured** in a certain unit, e.g.
when describing data throughput. Clients should be able to understand the following units for measured rates:

```
B     - bytes
KiB   - kibibytes = 2^10 bytes
MiB   - mebibytes = 2^20 bytes
GiB   - gibibytes = 2^30 bytes
TiB   - tebibytes = 2^40 bytes
PiB   - pebibytes = 2^50 bytes
EiB   - exbibytes = 2^60 bytes
```

Rates can have limits and/or track usage:

- A rate's **usage** is an ever-increasing counter of how many actions have been executed.
- **Rate limits** impose an upper bound on how many actions of that particular kind are allowed in a certain amount of
  time.

### Rate limits

A limit is defined by a budget of actions that can be executed at once, and a window of time in which that budget
replenishes. For example, a rate limit of `10/30s` means that 10 actions can be executed at once (this is sometimes
referred to as **burst budget**), and a fully emptied budget will fill back to the full 10 within 30 seconds. If actions
are executed continuously, one action would be allowed every three seconds on average. (In other words, Limes rate
limits always apply on sliding windows, not within fixed timeslots.)

On the API, windows are formatted as strings such as `30s`, that are a pair of an integer value and a time unit (`ms`
for milliseconds, `s` for seconds, `m` for minutes, or `h` for hours).

Rate usage is always tracked on the project level only, but rate limits can be defined both on the cluster level
(**global rate limit**) and on the project level:

- Global rate limits apply to all users in aggregate, to ensure that a service does not receive more requests or
  transfer more data than it can handle.
- Project-level rate limits only apply to users authenticated within that project, to ensure a fair distribution of
  usage among projects within a cluster.

Global rate limits, as well as the default values for project-level rate limits, are defined in the [service
configuration][rl-conf].

[rl-conf]: ../operators/config.md#rate-limits

## Common request headers

### X-Auth-Token

As with all OpenStack services, this header must always contain a Keystone token.

## Common query arguments

All GET endpoints accept the following optional query arguments:

| Argument | Description |
| -------- | ----------- |
| `service` | Limit query to rates in these services (e.g. `?service=compute&service=network`). |
| `area` | Limit query to rates in services in these areas. |

## Endpoints

### GET /v1/clusters/current

_Historical note: Multi-cluster support was removed from Limes._ It used to be possible to list data for multiple
clusters and query for cluster data using any configured cluster ID. By now, only the exact endpoint URL shown in the
heading is supported.

Query global rate limits for the cluster level. These rate limits apply to all users in aggregate.

Returns 200 (OK) on success and a JSON document like this:

```json
{
  "cluster": {
    "id": "current",
    "services": [
      {
        "type": "object-store",
        "area": "storage",
        "rates": [
          {
            "name": "service/shared/objects:create",
            "limit": 5000,
            "window": "1s"
          },
          {
            "name": "service/shared/objects:update",
            "limit": 10000,
            "window": "1s"
          },
          {
            "name": "service/shared/objects:delete",
            "limit": 5000,
            "window": "1s"
          },
          ...
        ]
      },
      ...
    ]
  }
}
```


The following fields can appear in the response body:

| Field | Type | Description |
| ----- | ---- | ----------- |
| `cluster.id` | string | The string "current". Other cluster IDs are not supported anymore. |
| `cluster.services` | list of objects | List of matching services that have rates. |
| `cluster.services[].type` | string | The type name of this service. |
| `cluster.services[].area` | string | The area name for this service. |
| `cluster.services[].rates` | list of objects | Information about a single rate in this service. Only rates that have global rate limits will be shown. |
| `cluster.services[].min_scraped_at`<br>`cluster.services[].max_scraped_at` | integer | UNIX timestamp range of when this service's rate usage values were last scraped across all projects in this cluster. |

The objects at `cluster.services[].rates[]` may contain the following fields:

| Field | Type | Description |
| ----- | ---- | ----------- |
| `name` | string | The name of this rate. |
| `limit` | unsigned integer | The budget part of the global rate limit for this rate. |
| `window` | string | The window part of the global rate limit for this rate. |

### GET /v1/domains/:domain\_id/projects

Query rate data for projects in a domain. Requires at least a domain-scoped token.

Returns 200 (OK) on success and a JSON document like this:

```json
{
  "projects": [
    {
      "id": "8ad3bf54-2401-435e-88ad-e80fbf984c19",
      "name": "example-project",
      "parent_id": "e4864dd1-1929-4b41-bb69-e5a724f20fa2",
      "services": [
        {
          "type": "compute",
          "area": "compute",
          "rates": [
            {
              "name": "service/compute/servers:create",
              "limit": 5,
              "window": "2m",
              "usage_as_bigint": "1069298"
            },
            {
              "name": "service/compute/servers/action:update/addFloatingIp",
              "limit": 2,
              "window": "1m"
            },
            {
              "name": "service/compute/servers/action:update/removeFloatingIp",
              "limit": 2,
              "window": "1m"
            }
          ],
          "scraped_at": 1486738206
        },
        ...
      ]
    },
    ...
  ]
}
```

The following fields can appear in the response body:

| Field | Type | Description |
| ----- | ---- | ----------- |
| `projects` | list of objects | List of projects in the given domain. |
| `projects[].id` | string | UUID of this project in Keystone. |
| `projects[].name` | string | Name of this project in Keystone. |
| `projects[].parent_id` | string | UUID of this project's parent object (either the parent project, or the domain) in Keystone. |
| `projects[].services` | list of objects | List of matching services that have rates. |
| `projects[].services[].type` | string | The type name of this service. |
| `projects[].services[].area` | string | The area name for this service. |
| `projects[].services[].rates` | list of objects | Information about a single rate in a matching service. |
| `projects[].services[].scraped_at` | timestamp | UNIX timestamp of when this service's rate usage values were last scraped. |

The objects at `projects[].services[].rates[]` may contain the following fields:

| Field | Type | Description |
| ----- | ---- | ----------- |
| `name` | string | The name of this rate. |
| `unit` | string | The unit for this rate (omitted if this rate is counted instead of
| `limit` | unsigned integer | **(L)** The budget part of the project-scoped rate limit for this rate. |
| `window` | string | **(L)** The window part of the project-scoped rate limit for this rate. |
| `default_limit` | unsigned integer | **(L)(D)** The budget part of the default project-scoped rate limit for this rate. |
| `default_window` | string | **(L)(D)** The window part of the default project-scoped rate limit for this rate. |
| `usage_as_bigint` | string | **(U)** The project-scoped usage value for this rate. (See below for why this is a string.) |

Notes:

- Fields marked with **(L)** are only shown for rates that have limits, and omitted for rates that only track usage.
- Fields marked with **(D)** are only shown for rates where the current project-scoped rate limit differs from the default.
- Fields marked with **(U)** are only shown for rates that track usage, and omitted for rates that only have limits.

The field `usage_as_bigint` is an ever-increasing counter, guaranteed to never reset. Most JSON parser libraries parse
integers into 64-bit-wide types, a size which can be reasonably expected to overflow esp. for rates relating to data
throughput. Therefore the `usage_as_bigint` field (as indicated by the name) is set up like a bigint and serialized as a
string. For now, clients SHOULD be able to handle at least 128-bit-wide unsigned integers in this field.

### POST /v1/domains/:domain\_id/projects/:project\_id/sync

Schedules a sync job that pulls rate usage data for this project from the backing services into Limes's local database.
Requires at least a project-admin token. When the job was scheduled successfully, returns 202 (Accepted).

If the project does not exist in Limes's database yet, query Keystone to see if this project was just created. If so,
create the project in Limes's database before returning 202 (Accepted).

### GET /v1/domains/:domain\_id/projects/:project\_id

Query rate data for a single project. Requires at least a project-scoped token.

Returns 200 (OK) on success and a JSON document with a similar structure as the one returned by the `GET
/v1/domains/:domain_id/projects` endpoint. Instead of a list of objects in the top-level field `projects`, only a single
such object with identical substructure will be returned in the top-level field `project`.

### PUT /v1/domains/:domain\_id/projects/:project\_id

Set rate limits for the given project. Requires a cloud-admin token, and a request body that is a JSON document like:

```json
{
  "project": {
    "services": [
      {
        "type": "compute",
        "area": "compute",
        "rates": [
          {
            "name": "service/compute/servers:create",
            "limit": 5,
            "window": "10m"
          }
        ]
      }
    ]
  }
}
```

The following fields can appear in the request body:

| Field | Type | Description |
| ----- | ---- | ----------- |
| `project` | object | Top-level grouping to match the response structure in the respective GET endpoint. |
| `project.services` | list of objects | List of services where rate limits shall be updated. |
| `project.services[].type` | string | The type name of this service. |
| `project.services[].rates` | list of objects | List of rates in this service where rate limits shall be updated. |

The objects at `projects[].services[].rates[]` may contain the following fields:

| Field | Type | Description |
| ----- | ---- | ----------- |
| `name` | string | The name of this rate. |
| `limit` | unsigned integer | The amount part of the requested rate limit for this rate. |
| `unit` | string | The unit for the value in the `limit` field (must be omitted if this rate is counted instead of measured, can be omitted to imply the rate's default unit). |
| `window` | string | The window part of the requested rate limit for this rate. |

On success, returns 202 (Accepted) and no response body. On error, 4xx responses may be returned. The exact response
status is determined in the same way as described for the respective `simulate-put` endpoint below.

### POST /v1/domains/:domain\_id/projects/:project\_id/simulate-put

Requires a similar token and request body like `PUT /v1/domains/:domain\_id/projects/:project\_id`, but does not attempt
to actually change any rate limits.

Returns 200 (OK) on success, or 4xx otherwise (see below). Result is a JSON document like:

```json
{
  "success": false,
  "unacceptable_rates": [
    {
      "service_type": "compute",
      "name": "service/compute/servers:create",
      "status": 403,
      "message": "user is not allowed to set compute rate limits"
    }
  ]
}
```

If `success` is true, the corresponding PUT request would have been accepted (i.e., produced a 202 response). If
`success` is false, `unacceptable_rates` contains one entry for each rate whose requested limit value was not accepted,
with the following fields:

| Field | Type | Description |
| ----- | ---- | ----------- |
| `service_type` | string | The service wherein the rate with the unacceptable rate limit is located. |
| `name` | string | The name of the rate with the unacceptable rate limit. |
| `status` | unsigned integer | An HTTP status code providing a broad classification of why the rate limit is not acceptable. |
| `message` | string | A human-readable message describing why the rate limit is not acceptable. |

The response status will be either 422 (Unprocessable Entity) if multiple unacceptable rates have different status
codes, or equal to the status code of the unacceptable rates if they all agree on one. For example, if all rates are
rejected with status 403, the entire request will have response status 403.

The following status codes are possible for unacceptable rates:

- 403 (Forbidden) indicates that a higher permission level (e.g. a cloud-admin token instead of a domain-admin token) is
  needed to set the requested rate limit value, or that this rate limit is not configurable at all.
- More status codes may be added in the future.

### GET /v1/admin/scrape-errors

Shows information about project rate data scrape errors. Requires a cloud-admin token. This is intended to give operators a view of rate data scrape errors for all services across all projects.

In order to avoid excessively large responses, identical scrape errors for multiple project services of the same type
will be grouped into one item and an additional field will be included in the response to indicate the number of
projects affected by this particular scrape issue.

Returns 200 (OK) on success. Result is a JSON document like:

```json
{
  "rate_scrape_errors": [
    {
      "project": {
        "id": "8ad3bf54-2401-435e-88ad-e80fbf984c19",
        "name": "example-project",
        "domain": {
          "id": "d5fbe312-1f48-42ef-a36e-484659784aa0",
          "name": "example-domain"
        }
      },
      "service_type": "object-store",
      "checked_at": 1486738599,
      "message": "could not scrape object-store due to some issue"
    },
    {
      "project": {
        "id": "8ad3bf54-2401-435e-88ad-e80fbf984c19",
        "name": "example-project-2",
        "domain": {
          "id": "d5fbe312-1f48-42ef-a36e-484659784aa0",
          "name": "example-domain"
        }
      },
      "affected_projects": 2,
      "service_type": "compute",
      "checked_at": 1486738599,
      "message": "json: cannot unmarshal number into Go struct field"
    }
  ]
}
```

The following fields can appear in the response body:

| Field | Type | Description |
| ----- | ---- | ----------- |
| `rate_scrape_errors` | list of objects | Errors encountered during rate scraping. |

The objects at `rate_scrape_errors[]` may contain the following fields:

| Field | Type | Description |
| ----- | ---- | ----------- |
| `project` | object | Metadata for the project (or, for aggregated errors, one of the projects) where this rate scrape error was observed. |
| `project.id` | string | UUID of this project in Keystone. |
| `project.name` | string | Name of this project in Keystone. |
| `project.domain` | object | The Keystone domain where this project resides. |
| `project.domain.id` | string | UUID of this domain in Keystone. |
| `project.domain.name` | string | Name of this domain in Keystone. |
| `affected_projects` | unsigned integer | The number of projects where this rate scrape error was observed. Only shown when larger than 1. |
| `service_type` | string | Type name of the service where this rate scrape error was observed. |
| `checked_at` | integer | UNIX timestamp of the instant when this rate scrape error was observed in the specified project and service. |
| `message` | string | The exact error message that was observed. |
