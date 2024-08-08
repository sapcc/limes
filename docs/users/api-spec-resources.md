# Limes Resource API specification

The URLs indicated in the headers of each section are relative to the endpoint URL advertised in the Keystone
catalog under the service type `resources`.

Where permission requirements are indicated, they refer to the default policy. Limes operators can configure their
policy differently, so that certain requests may require other roles or token scopes.

Use the table of contents icon
<img src="https://github.com/github/docs/raw/main/assets/images/table-of-contents.png" width="25" height="25" />
near the top left corner of this document to jump to a specific section on this page.

## Concepts

The Limes Resource API deals with **resources**. A resource is any countable or measurable kind of entity that can be
distributed within a cloud.

Resources are grouped into the **services** that manage their allocation. Services are identified by a type string,
which is usually identical to the type of the respective service entry in the Keystone service catalog. Limes further
groups services into **areas**: For example, the services for block storage, shared filesystem storage and object
storage are grouped into the "storage" area. Within a service, resources may optionally be grouped into **categories**.
Areas and categories serve purely as hints for UIs that want to group resources in a human-accessible way.

### Quantifying resources

Limes tracks three different kinds of quantity for each resource:

- **capacity**: how much of a resource exists across the extent of the whole cloud
- **quota**: how much of a resource a certain project or domain is entitled to use
- **usage**: how much of a resource a certain project or domain is currently using

When distributing quota, Limes follows Keystone's structure of projects within domains. Different quantities are
measured on different levels of this hierarchy:

- On the project level, the **project quota** is managed by Limes. Since resource reservations are not performed by
  Limes itself, but by the respective backing service managing the resource in question, Limes continually ensures that
  the **backend quota** (the project's quota for that resource in the backing service) stays in sync with the quota
  managed in Limes. Limes also scrapes **usage** data from the backing service for each project. The backing service is
  responsible for ensure that the usage for each project resource does not exceed the respective quota.
- For historical reasons, the domain level has two attributes called **domain quota** and **projects quota**. Those are
  currently both equal to the sum of quotas over all projects within the domain.
- The cluster level is where **capacity** is reported. The cluster level also reports the **domains quota**, the sum of
  the domain quota across all domains, which by now is just equal to the sum of the project quota across all projects.

For resources that are **counted**, all these measurements are unitless. However, resources can also be **measured** in
a certain unit, which then applies to all the aforementioned measurements. Clients should be prepared to handle the
following values for a resource's unit:

```
B     - bytes
KiB   - kibibytes = 2^10 bytes
MiB   - mebibytes = 2^20 bytes
GiB   - gibibytes = 2^30 bytes
TiB   - tebibytes = 2^40 bytes
PiB   - pebibytes = 2^50 bytes
EiB   - exbibytes = 2^60 bytes
```

### Physical usage

In addition to usage, some resources also report **physical usage**. In this case, "usage" refers to fixed resource
allocations, and "physical usage" refers to how much of the fixed resource allocation is actually used. For example,
an NFS share will have "usage" according to its size and "physical usage" according to how much data is actually stored
in it. Quota always relates to "usage": For example, consider a project with for 20 GiB worth of NFS shares that has
three NFS shares with a size of 5 GiB each and utilization of 2 GiB each. Therefore, the usage is 15 GiB and only one
more 5 GiB share can be created, even though the physical usage is only 6 GiB.

### Commitments

Resources can be configured to allow **commitments**. A commitment expresses that the project owner promises to use a
certain amount of a resource for a fixed time frame. Commitments usually provide a price discount, with the caveat that
the committed usage will be billed even if the real usage is lower. Commitments also provide a stronger guarantee that
the respective amount of resource will be available to the project throughout the commitment's time frame.

Commitments are always tied to an availability zone to aid in demand planning on the availability zone level.

Commitments follow a simple state machine:

* `-> unconfirmed`: Commitments are created by a project administrator.
  Price discounts and capacity guarantees apply only once the commitment is confirmed.
  Creating an unconfirmed commitment is only possible if the commitment is created with a `confirm_by` timestamp.
  Such commitments are intended for demand management and forecasting.
* `unconfirmed -> confirmed`: Once the underlying capacity has been reserved for the project, the commitment is confirmed.
* `-> confirmed`: Commiments can also be created by a project administrator in an immediately-confirmed state,
  if the respective capacity can be reserved for the project immediately.
* `confirmed -> expired`: Once the commitment's duration elapses, the price discount and capacity guarantee elapse.
  The duration until expiry counts starting from the state transition into `confirmed`.

### Subresources

For some resources, Limes can report **subresources**. Subresources are a way to break down the project-level usage of
resources into distinct entities with their own set of attributes. Subresources are mostly intended for billing: A
billing service can use data collected by Limes to create itemized bills, or to price resources depending on their
attributes. (For example, a floating IP in an external network may be more expensive than one from an internal network.)

Subresources will be displayed on GET requests on the project level when the `?detail` query parameter is given (no
value is required).

### Subcapacities

For some resources, Limes can report **subcapacities**. Subcapacities are a way to break down the cluster-level capacity
of resources into distinct entities with their own set of attributes. For example, the `compute/cores` resource can have
its capacity broken down into subcapacities for each Nova hypervisor. This allows other services to more effectively
reuse the capacity computations performed by Limes, without having to duplicate the internal business logic.

### Overcommit

Limes can add an **overcommit factor** on top of the capacity that the backing service actually reports. If an
overcommit factor is configured, the capacity value reported by the backing service will be shown as **raw capacity**,
and the main capacity value will be the raw capacity multiplied by the overcommit factor.

## Common request headers

### X-Auth-Token

As with all OpenStack services, this header must always contain a Keystone token.

## Common query arguments

All GET endpoints accept the following optional query arguments:

| Argument | Description |
| -------- | ----------- |
| `service` | Limit query to resources in these services (e.g. `?service=compute&service=network`). |
| `resource` | Limit query to the specified resources (e.g. `?service=compute&resource=cores&resource=ram`). |
| `area` | Limit query to resources in services in these areas. |
| `detail` | If given (without argument, as `?detail`), include subresources and subcapacities in the response (if applicable). |

## Endpoints

### GET /v1/clusters/current

_Historical note: Multi-cluster support was removed from Limes._ It used to be possible to `GET /v1/clusters` and `GET
/v1/clusters/:cluster_id` with a cluster ID other than "current". By now, only the exact endpoint URL shown in the
heading is supported.

Query data for the cluster level. Usually accessible with any token, but `?detail` may require a cloud-admin token.

Returns 200 (OK) on success. Result is a JSON document like:

```json
{
  "cluster": {
    "id": "current",
    "services": [
      {
        "type": "compute",
        "area": "compute",
        "resources": [
          {
            "name": "instances",
            "domains_quota": 20,
            "usage": 1
          },
          {
            "name": "cores",
            "capacity": 1000,
            "per_availability_zone": [
              {
                "name": "az-one",
                "capacity": 500,
                "usage": 0
              },
              {
                "name": "az-two",
                "capacity": 500,
                "usage": 2
              }
            ],
            "domains_quota": 100,
            "usage": 2
          },
          ...
        ],
        "max_scraped_at": 1486738599,
        "min_scraped_at": 1486728599
      },
      ...
    ],
    "max_scraped_at": 1486712957,
    "min_scraped_at": 1486701582
  }
}
```

The following fields can appear in the response body:

| Field | Type | Description |
| ----- | ---- | ----------- |
| `cluster.id` | string | The string "current". Other cluster IDs are not supported anymore. |
| `cluster.min_scraped_at`<br>`cluster.max_scraped_at` | integer | UNIX timestamp range of when this cluster's capacity values were last scraped. |
| `cluster.services` | list of objects | List of matching services that have resources. |
| `cluster.services[].type` | string | The type name of this service. |
| `cluster.services[].area` | string | The area name for this service. |
| `cluster.services[].resources` | list of objects | Information about a single matching resource in this service. |
| `cluster.services[].min_scraped_at`<br>`cluster.services[].max_scraped_at` | integer | UNIX timestamp range of when this service's backend quota and usage values were last scraped across all projects in this cluster. |

The objects at `cluster.services[].resources[]` may contain the following fields:

| Field | Type | Description |
| ----- | ---- | ----------- |
| `name` | string | The name of this resource. |
| `unit` | string | The unit of this resource (only shown for measured resources). |
| `category` | string | The category of this resource (only shown when there is one). |
| `contained_in` | string | Obsolete. If this field is shown at all, ignore it. |
| `quota_distribution_model` | string | The resource's [quota distribution model](#quota-distribution-model). The only possible value is "autogrow". |
| `capacity` | unsigned integer | The available capacity for this resource. |
| `raw_capacity` | unsigned integer | The available raw capacity for this resource (only shown for [overcommitted resources](#overcommit)). |
| `per_availability_zone` | list of objects | A breakdown of this resource's capacity by availability zone (only shown for resources supporting a breakdown by AZ). |
| `domains_quota` | unsigned integer | The sum of all quotas for this resource across all projects. (The name is for historical reasons.) |
| `usage` | unsigned integer | The sum of all usage values for this resource across all projects in all domains. |
| `physical_usage` | unsigned integer | The sum of all physical usage values for this resource across all projects in all domains (only shown for [resources that report physical usage](#physical-usage)). |
| `subcapacities` | list of objects | The subcapacities for this resource (only shown if `?detail` is given in the query and the resource supports [subcapacity reporting](#subcapacities)). |

The objects at `cluster.services[].resources[].per_availability_zones[]` may contain the following fields:

| Field | Type | Description |
| ----- | ---- | ----------- |
| `name` | string | The name of this availability zone (AZ). |
| `capacity` | unsigned integer | The available capacity for this resource in this AZ. |
| `raw_capacity` | unsigned integer | The available raw capacity for this resource in this AZ (only shown for [overcommitted resources](#overcommit)). |
| `usage` | unsigned integer | The part of all usage for this resource across all projects in all domains that is located in this AZ. |

### GET /v1/domains

Query resource data for domains. Requires a cloud-admin token.

Returns 200 (OK) on success. Result is a JSON document like:

```json
{
  "domains": [
    {
      "id": "d5fbe312-1f48-42ef-a36e-484659784aa0",
      "name": "example-domain",
      "services": [
        {
          "type": "compute",
          "area": "compute",
          "resources": [
            {
              "name": "instances",
              "quota": 20,
              "projects_quota": 5,
              "usage": 1
            },
            {
              "name": "cores",
              "quota": 100,
              "projects_quota": 20,
              "usage": 2,
              "backend_quota": 50
            },
            {
              "name": "ram",
              "unit": "MiB",
              "quota": 204800,
              "projects_quota": 10240,
              "usage": 2048,
              "physical_usage": 1376
            }
          ],
          "max_scraped_at": 1486738599,
          "min_scraped_at": 1486728599
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
| `domains` | list of objects | List of domains. |
| `domains[].id` | string | UUID of this domain in Keystone. |
| `domains[].name` | string | Name of this domain in Keystone. |
| `domains[].services` | list of objects | List of matching services that have resources. |
| `domains[].services[].type` | string | The type name of this service. |
| `domains[].services[].area` | string | The area name for this service. |
| `domains[].services[].resources` | list of objects | Information about a single matching resource in this service. |
| `domains[].services[].min_scraped_at`<br>`domains[].services[].max_scraped_at` | integer | UNIX timestamp range of when this service's backend quota and usage values were last scraped across all projects in this domain. |

The objects at `domains[].services[].resources[]` may contain the following fields:

| Field | Type | Description |
| ----- | ---- | ----------- |
| `name` | string | The name of this resource. |
| `unit` | string | The unit of this resource (only shown for measured resources). |
| `category` | string | The category of this resource (only shown when there is one). |
| `contained_in` | string | Obsolete. If this field is shown at all, ignore it. |
| `quota_distribution_model` | string | The resource's [quota distribution model](#quota-distribution-model). The only possible value is "autogrow". |
| `quota` | unsigned integer | The sum of all project quotas for this resource across all projects in this domain. |
| `projects_quota` | unsigned integer | **Deprecated.** Equal to `quota`. |
| `usage` | unsigned integer | The sum of all usage values for this resource across all projects in this domain. |
| `physical_usage` | unsigned integer | The sum of all physical usage values for this resource across all projects in this domain (only shown for [resources that report physical usage](#physical-usage)). |
| `backend_quota` | unsigned integer | The sum of all nonzero backend quota values for this resource across all projects in this domain (only shown if this value differs from the value in the `quota` field). |
| `infinite_backend_quota` | boolean | Whether any project in this domain has a backend quota value of -1 (only shown if true). |

### GET /v1/domains/:domain\_id

Query resource data for a single domain. Requires at least a domain-scoped token.

Returns 200 (OK) on success and a JSON document with a similar structure as the one returned by the `GET /v1/domains`
endpoint. Instead of a list of objects in the top-level field `domains`, only a single such object with identical
substructure will be returned in the top-level field `domain`.

### PUT /v1/domains/:domain\_id

**Deprecated.** Always returns 405 (Method Not Allowed) because support for setting quotas manually has been removed from Limes.

### POST /v1/domains/:domain\_id/simulate-put

**Deprecated.** Always returns 405 (Method Not Allowed) because support for setting quotas manually has been removed from Limes.

### POST /v1/domains/discover

Requires a cloud-admin token. Queries Keystone in order to discover newly-created domains that Limes does not yet know
about.

When no new domains were found, returns 204 (No Content). Otherwise, returns 202 (Accepted) and a JSON document listing
the newly discovered domains:

```json
{
  "new_domains": [
    { "id": "94cfaed4-3062-47d2-9299-ef599d5ffbfb" },
    { "id": "b66dcb34-ea53-4872-b99b-123ae9c581b4" },
    ...
  ]
}
```

When the call returns, quota/usage data for these domains will not yet be available (thus return code 202).

*Rationale:* When a cloud administrator creates a new domain, he might want to assign quota to that domain immediately
after that, but he can only do so after Limes has discovered the new domain. Limes will do so automatically after some
time through scheduled auto-discovery, but this call can be used to reduce the waiting time.

### GET /v1/domains/:domain\_id/projects

Query resource data for projects. Requires at least a domain-scoped token.

Returns 200 (OK) on success. Result is a JSON document like:

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
          "resources": [
            {
              "name": "instances",
              "quota": 5,
              "usable_quota": 5,
              "usage": 1
            },
            {
              "name": "cores",
              "quota": 20,
              "usable_quota": 20,
              "usage": 2,
              "backend_quota": 50
            },
            {
              "name": "ram",
              "unit": "MiB",
              "quota": 10240,
              "usable_quota": 10240,
              "usage": 2048,
              "physical_usage": 1058
            }
          ],
          "scraped_at": 1486738599
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
| `projects[].services` | list of objects | List of matching services that have resources. |
| `projects[].services[].type` | string | The type name of this service. |
| `projects[].services[].area` | string | The area name for this service. |
| `projects[].services[].resources` | list of objects | Information about a single resource in a matching service. |
| `projects[].services[].scraped_at` | timestamp | UNIX timestamp of when this service's quota and usage values were last scraped. |

The objects at `projects[].services[].resources[]` may contain the following fields:

| Field | Type | Description |
| ----- | ---- | ----------- |
| `name` | string | The name of this resource. |
| `unit` | string | The unit of this resource (only shown for measured resources). |
| `category` | string | The category of this resource (only shown when there is one). |
| `contained_in` | string | Obsolete. If this field is shown at all, ignore it. |
| `quota_distribution_model` | string | The resource's [quota distribution model](#quota-distribution-model). The only possible value is "autogrow". |
| `quota` | unsigned integer | The granted quota for this resource in this project. |
| `usable_quota` | unsigned integer | **Deprecated.** Always equal to `quota`. |
| `usage` | unsigned integer | The usage of this resource in this project. |
| `physical_usage` | unsigned integer | The physical usage of this resource in this project (only shown for [resources that report physical usage](#physical-usage)). |
| `backend_quota` | integer | The backend quota for this resource in this project (only shown if the value differs from `usable_quota`). Infinite backend quota is represented by the value `-1`. |
| `subresources` | list of objects | The subresources for this resource (only shown if `?detail` is given in the query and the resource supports [subresource reporting](#subresources)). |

### GET /v1/domains/:domain\_id/projects/:project\_id

Query resource data for a single project. Requires at least a project-scoped token.

Returns 200 (OK) on success and a JSON document with a similar structure as the one returned by the `GET
/v1/domains/:domain_id/projects` endpoint. Instead of a list of objects in the top-level field `projects`, only a single
such object with identical substructure will be returned in the top-level field `project`.

### PUT /v1/domains/:domain\_id/projects/:project\_id

**Deprecated.** Always returns 405 (Method Not Allowed) because support for setting quotas manually has been removed from Limes.

### POST /v1/domains/:domain\_id/projects/:project\_id/simulate-put

**Deprecated.** Always returns 405 (Method Not Allowed) because support for setting quotas manually has been removed from Limes.

### POST /v1/domains/:domain\_id/projects/discover

Requires a domain-admin token for the specified domain. Queries Keystone in order to discover newly-created projects in
this domain that Limes does not yet know about. This works exactly like `POST /domains/discover`, except that the JSON
document will list `new_projects` instead of `new_domains`.

*Rationale:* Same as for domain discovery: The domain admin might want to assign quotas immediately after creating a new
project.

### POST /v1/domains/:domain\_id/projects/:project\_id/sync

Schedules a sync job that pulls backend quota and usage data for this project from the backing services into Limes's
local database, and applies the quota expected by Limes if the backend quota diverges. Requires at least a project-admin
token. When the job was scheduled successfully, returns 202 (Accepted).

If the project does not exist in Limes's database yet, query Keystone to see if this project was just created. If so,
create the project in Limes's database before returning 202 (Accepted).

*Rationale:* When a project administrator wants to adjust her project's quotas, they might discover that the usage data
shown by Limes is out-of-date. They can then use this call to refresh the usage data in order to make a more informed
decision about how to adjust her quotas.

### GET /v1/domains/:domain\_id/projects/:project\_id/commitments

List commitments for a single project. Requires at least a project-scoped token.

Returns 200 (OK) on success. Result is a JSON document like:

```json
{
  "commitments": [
    {
      "id": 42023,
      "service_type": "compute",
      "resource_name": "cores",
      "availability_zone": "west-1",
      "amount": 100,
      "duration": "2 years",
      "requested_at": 1696604400,
      "confirmed_at": 1696636800,
      "expires_at": 1759795200
    }
  ]
}
```

The following fields can appear in the response body:

| Field | Type | Description |
| ----- | ---- | ----------- |
| `commitments` | list of objects | List of commitments in the given project. |
| `commitments[].id` | integer | A unique identifier for this commitment. |
| `commitments[].service_type`<br>`commitments[].resource_name` | string | The resource for which usage is committed. |
| `commitments[].availability_zone` | string | The availability zone in which usage is committed. |
| `commitments[].amount` | integer | The amount of usage that was committed to. |
| `commitments[].unit` | string | For measured resources, the unit for this resource. The value from the `amount` field is measured in this unit. |
| `commitments[].duration` | string | The requested duration of this commitment, expressed as a comma-separated sequence of positive integer multiples of time units like "1 year, 3 months". Acceptable time units include "second", "minute", "hour", "day", "month" and "year". |
| `commitments[].created_at` | integer | UNIX timestamp when this commitment was created. |
| `commitments[].confirm_by` | integer | UNIX timestamp when this commitment should be confirmed. Only shown if this was given when creating the commitment, to delay confirmation into the future. |
| `commitments[].confirmed_at` | integer | UNIX timestamp when this commitment was confirmed. Only shown after confirmation. |
| `commitments[].expires_at` | integer | UNIX timestamp when this commitment is set to expire. Note that the duration counts from `confirm_by` (or from `created_at` for immediately-confirmed commitments) and is calculated at creation time, so this is also shown on unconfirmed commitments. |
| `commitments[].transferable` | boolean | Whether the commitment is marked for transfer to a different project. Transferable commitments do not count towards quota calculation in their project, but still block capacity and still count towards billing. Not shown if false. |

### POST /v1/domains/:domain\_id/projects/:project\_id/commitments/new

Creates a new commitment within the given project. Requires a project-admin token, and a request body that is a JSON document like:

```json
{
  "commitment": {
    "service_type": "compute",
    "resource_name": "cores",
    "availability_zone": "west-1",
    "amount": 100,
    "duration": "2 years"
  }
}
```

The following fields can appear in the request body:

| Field | Type | Description |
| ----- | ---- | ----------- |
| `commitment.id` | integer | A unique identifier for this commitment. |
| `commitment.service_type`<br>`commitments[].resource_name` | string | The resource for which usage is committed. |
| `commitment.availability_zone` | string | The availability zone in which usage is committed. |
| `commitment.amount` | integer | The amount of usage that was committed to. For measured resources, this is measured in the resource's unit as reported on the project resource. |
| `commitment.duration` | string | The requested duration of this commitment. This must be one of the options reported on the project resource. |
| `commitment.confirm_by` | integer | UNIX timestamp of the time by which this commitment should be confirmed. If not given, Limes will immediately try to confirm this commitment, and return an error if there is not enough committable capacity. If given, Limes will confirm this commitment after `confirm_by` has passed, as soon as enough committable capacity is available. |

Returns 201 (Created) on success. Result is a JSON document like:

```json
{
  "commitment": {
    "id": 42023,
    "service_type": "compute",
    "resource_name": "cores",
    "availability_zone": "west-1",
    "amount": 100,
    "duration": "2 years",
    "requested_at": 1696604400
  }
}
```

The `commitment` object has the same structure as the `commitments[]` objects in `GET /v1/domains/:domain_id/projects/:project_id/commitments`.
If `confirm_by` was given, a successful response will include the `confirmed_at` timestamp.

### POST /v1/domains/:domain\_id/projects/:project\_id/commitments/can-confirm

Checks if a new commitment within the given project could be confirmed immediately.
Requires a project-admin token, and a request body that is a JSON document with the same contents as for `POST /v1/domains/:domain\_id/projects/:project\_id/commitments/new`, except that the `commitment.confirm_by` attribute must not be set.

Returns 200 (OK) on success, and a JSON document like `{"result":true}` or `{"result":false}`.

The `result` field indicates whether this commitment can be created without a `confirm_by` attribute, that is, confirmed immediately upon creation.

### POST /v1/domains/:id/projects/:id/commitments/:id/start-transfer
Prepares a commitment to be transferred from a source project to a target project. Requires a project-admin token, and a request body that is a JSON document like:
```json
{
  "commitment": {
    "amount": 100,
    "transfer_status": "unlisted"
  }
}
```
If the amount to transfer is equal to the commitment, the whole commitment will be marked as transferrable. If the amount is less than the commitment, the commitment will be split in two and the requested amount will be marked as transferrable.
The transfer status indicates if the commitment stays `unlisted` (private) or `public`.
The response is a JSON of the commitment including the following fields that identify a commitment in its transferrable state:
```json
{
  "commitment": {
    "transfer_token": "token",
    "transfer_status": "unlisted"
  }
}
```
### POST /v1/domains/:id/projects/:id/transfer-commitment/:id
Transfers the commitment from a source project to a target project.
Requires a project-admin token.
Requires a transfer token in the request header:
`Transfer-Token: [value]`.
This endpoint receives the target project ID, but the commitment ID from the source project.
Requires a generated token from the API: `/v1/domains/:id/projects/:id/commitments/:id/start-transfer`.
On success the API clears the `transfer_token` and `transfer_status` from the commitment.
After that, it returns the commitment as a JSON document.  

### GET /v1/commitments/:token
To ensure that a commitment can be checked for its `resource` type or `availability zone` before it gets transferred to a target project, this endpoint fetches the target commitment by its respective token.

### DELETE /v1/domains/:domain\_id/projects/:project\_id/commitments/:id

Deletes a commitment within the given project. Requires a cloud-admin token. On success, returns 204 (No Content).

Only unconfirmed commitments may be deleted. If the commitment has already been confirmed, returns 403 (Forbidden).

### GET /v1/inconsistencies

Requires a cloud-admin token. Detects inconsistent quota setups for domains and projects in the current cluster. The following
situations are considered:

1. `domain_quota_overcommitted` &ndash; The total quota of some resource across all projects in some domain exceeds the
   quota of that resource for the domain. (In other words, the domain has distributed more quota to its projects than it
   has been given.) This may happen when new projects are created and their quota is initialized because of constraints
   configured by a cloud administrator.
2. `project_quota_overspent` &ndash; The quota of some resource in some project is lower than the usage of that resource
   in that project. This may happen when someone else changes the quota in the backend service directly and increases
   usage before Limes intervenes.
3. `project_quota_mismatch` &ndash; The quota of some resource in some project differs from the backend quota for that
   resource and project. This may happen when Limes is unable to write a changed quota value into the backend, for
   example because of a service downtime.

Returns 200 (OK) on success. Result is a JSON document like:

```json
{
  "inconsistencies": {
    "domain_quota_overcommitted": [],
    "project_quota_overspent": [
      {
        "project": {
          "id": "8ad3bf54-2401-435e-88ad-e80fbf984c19",
          "name": "example-project",
          "domain": {
            "id": "d5fbe312-1f48-42ef-a36e-484659784aa0",
            "name": "example-domain"
          }
        },
        "service": "compute",
        "resource": "ram",
        "unit": "GiB",
        "quota": 250,
        "usage": 300
      },
      ...
    ],
    "project_quota_mismatch": [
      {
        "project": {
          "id": "8ad3bf54-2401-435e-88ad-e80fbf984c19",
          "name": "example-project",
          "domain": {
            "id": "d5fbe312-1f48-42ef-a36e-484659784aa0",
            "name": "example-domain"
          }
        },
        "service": "object-store",
        "resource": "storage",
        "unit": "B",
        "quota": 12345678,
        "backend_quota": 1234567
      },
      ...
    ]
  }
}
```

The field `inconsistencies.domain_quota_overcommitted` always contains an empty list for backwards-compatibility reasons.

The objects at `inconsistencies.project_quota_overspent[]` and `inconsistencies.project_quota_mismatch[]` may contain
the following fields:

| Field | Type | Description |
| ----- | ---- | ----------- |
| `project` | string | Metadata for the affected project. |
| `project.id` | string | UUID of this project in Keystone. |
| `project.name` | string | Name of this project in Keystone. |
| `project.domain` | string | Metadata for this project's domain. |
| `project.domain.id` | string | UUID of this domain in Keystone. |
| `project.domain.name` | string | Name of this domain in Keystone. |
| `service` | string | The type name of the service that contains the resource with the overspent project quota. |
| `resource` | string | The name of the resource with the overspent project quota. |
| `unit` | string | The unit of this resource (only shown for measured resources). |
| `quota` | unsigned integer | The quota for the affected resource in the affected project. |
| `usage` | unsigned integer | The usage for the affected resource in the affected project (only for `project_quota_overspent`). |
| `backend_quota` | unsigned integer | The backend quota for the affected resource in the affected project (only for `project_quota_mismatch`). Infinite backend quota is represented by the value `-1`. |

Each entry in those three lists concerns exactly one resource in one project or domain. If multiple resources in the
same project are inconsistent, they will appear as multiple entries. Like in the example above, the same project and
resource may appear in both `project_quota_overspent` and `project_quota_mismatch` if `usable_quota < usage <
backend_quota`.

### GET /v1/admin/scrape-errors

Requires a cloud-admin token. Shows information about project scrape errors. This is intended to give operators a view
of scrape errors for all services across all projects.

In order to avoid excessively large responses, identical scrape errors for multiple project services of the same type
will be grouped into one item and an additional field will be included in the response, `affected_projects`, which will
reflect the number of projects affected by this particular scrape issue.

Returns 200 (OK) on success. Result is a JSON document like:

```json
{
  "scrape_errors": [
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
| `scrape_errors` | list of objects | Errors encountered during quota/usage scraping. |

The objects at `scrape_errors[]` may contain the following fields:

| Field | Type | Description |
| ----- | ---- | ----------- |
| `project` | object | Metadata for the project (or, for aggregated errors, one of the projects) where this resource scrape error was observed. |
| `project.id` | string | UUID of this project in Keystone. |
| `project.name` | string | Name of this project in Keystone. |
| `project.domain` | string | Metadata for this project's domain. |
| `project.domain.id` | string | UUID of this domain in Keystone. |
| `project.domain.name` | string | Name of this domain in Keystone. |
| `affected_projects` | unsigned integer | The number of projects where this resource scrape error was observed. Only shown when larger than 1. |
| `service_type` | string | Type name of the service where this resource scrape error was observed. |
| `checked_at` | integer | UNIX timestamp of the instant when this resource scrape error was observed in the specified project and service. |
| `message` | string | The exact error message that was observed. |
