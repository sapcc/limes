# Public API specification

The URLs indicated in the headers of each section are relative to the endpoint URL advertised in the Keystone
catalog under the service type `resources`.

Where permission requirements are indicated, they refer to the default policy. Limes operators can configure their
policy differently, so that certain requests may require other roles or token scopes.

## Request headers

### X-Auth-Token

As with all OpenStack services, this header must contain a Keystone token.

### X-Limes-Cluster-Id

Each Limes API is bound to a certain OpenStack cluster, usually the one where it is configured in the service catalog.
To make a request concerning a domain or project in a different cluster, the `X-Limes-Cluster-Id` header must be given.
Using this header requires special permission (usually a cloud-admin token).

## GET /v1/domains/:domain\_id/projects
## GET /v1/domains/:domain\_id/projects/:project\_id

Query data for projects in a domain. `:project_id` is optional for domain admins. With domain admin token, shows
projects in that token's domain. With project member permission, shows that token's project only. Arguments:

* `service`: Limit query to resources in this service (e.g. `?service=compute`). May be given multiple times.
* `area`: Limit query to resources in services in this area. May be given multiple times.
* `resource`: When combined, with `?service=`, limit query to that resource
  (e.g. `?service=compute&resource=instances`). May be given multiple times.
* `detail`: If given, list subresources for resources that support it. (See subheading below for details.)

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
              "usage": 1
            },
            {
              "name": "cores",
              "quota": 20,
              "usage": 2,
              "backend_quota": 50
            },
            {
              "name": "ram",
              "unit": "MiB",
              "quota": 10240,
              "usage": 2048
            }
          ],
          "scraped_at": 1486738599
        },
        {
          "type": "object-store",
          "area": "storage",
          "resources": [
            {
              "name": "capacity",
              "unit": "B",
              "quota": 1073741824,
              "usage": 104857600
            }
          ],
          "scraped_at": 1486733778
        },
        ...
      ]
    },
    ...
  ]
}
```

If `:project_id` was given, the outer key is `project` and its value is the object without the array surrounding it.

On the project level, the `id`, `name` and `parent_id` from Keystone are shown. (The parent ID refers to the parent
project if there is one, otherwise it is identical to the domain ID.)

Quota/usage data for the project is ordered into `services`, then into `resources`. In the example above, services
include `compute` and `object_storage`, and the `compute` service has three resources, `instances`, `cores` and `ram`.
The service's `type` attribute will be identical to the same field in the Keystone service catalog. Through the `area`
attribute, services can be grouped into areas like `network` or `storage` for presentational purposes.

The data for each resource must include `quota` and `usage`. If the resource is not counted, but measured in a certain
unit, it will be given as `unit`. Clients should be prepared to handle at least the following values for `unit`:

    B     - bytes
    KiB   - kibibytes = 2^10 bytes
    MiB   - mebibytes = 2^20 bytes
    GiB   - gibibytes = 2^30 bytes
    TiB   - tebibytes = 2^40 bytes
    PiB   - pebibytes = 2^50 bytes
    EiB   - exbibytes = 2^60 bytes

Besides `unit`, there may be another informational field called `category`. If present, UIs can use the string value in
this field to divide resources from the same service into logical groups for presentational purposes. For example, the
service type `network` advertises resources with the category strings `networking` and `loadbalancing`, since these
topics are cleanly separable from each other.

Limes tracks quotas in its local database, and expects that the quota values in the backing services may only be
manipulated by the Limes service user, but not by the project's members or admins. If, nonetheless, Limes finds the
backing service to use a different quota value than what Limes expected, it will be shown in the `backend_quota` key, as
shown in the example above for the `compute/cores` resource. If a `backend_quota` value exists, a Limes client should
display a warning message to the user.

The `scraped_at` timestamp for each service denotes when Limes last checked the quota and usage values in the backing
service. The value is a standard UNIX timestamp (seconds since `1970-00-00T00:00:00Z`).

Valid values for quotas include all non-negative numbers. Backend quotas can also have the special value `-1` which
indicates an infinite or disabled quota.

TODO: Might need to add ordering and pagination to this at some point.

### Subresources

If the `?detail` query parameter is given (no value is required), countable resources may be further broken down into
*subresources*, i.e. entities of this countable resource with their own set of attributes. Intended usecases for
subresource include billing services using data collected by Limes to create itemized bills, or to bill resources
depending on their attributes. (For example, a floating IP in an external network may be more expensive than one from an
internal network.)

Subresources will only be displayed for supported resources, and only if scraping has been enabled for that subresource
in Limes' configuration. If enabled, the resource will have a `subresources` key containing an array of objects. For
example, extending the example from above, the `projects[0].services[0].resources[0]` object might look like this:

```json
{
  "name": "instances",
  "quota": 5,
  "usage": 1,
  "subresources": [
    { "id": "ad87bb8a-5864-4905-b099-40b9f2b49bf9", "name": "testvm", "cores": 2, "ram": 2048 }
  ]
}
```

The fields in the subresource objects are specific to the resource type, and are not mandated by this specification.
Please refer to the [documentation for the quota plugin that generates it](../operators/config.md) for details.

## GET /v1/domains
## GET /v1/domains/:domain\_id

Query data for domains. `:domain_id` is optional for cloud admins. With cloud admin token, shows all domains. With
domain admin token, shows that token's domain only. Arguments:

* `service`: Limit query to resources in this service. May be given multiple times.
* `area`: Limit query to resources in services in this area. May be given multiple times.
* `resource`: When combined, with `?service=`, limit query to that resource.

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
              "usage": 2048
            }
          ],
          "max_scraped_at": 1486738599,
          "min_scraped_at": 1486728599
        },
        {
          "type": "object-store",
          "resources": [
            {
              "name": "capacity",
              "unit": "B",
              "quota": 107374182400,
              "projects_quota": 1073741824,
              "usage": 104857600
            }
          ],
          "max_scraped_at": 1486733778,
          "min_scraped_at": 1486723778
        }
        ...
      ]
    },
    ...
  ]
}
```

If `:domain_id` was given, the outer key is `domain` and its value is the object without the array surrounding it.

Looks a lot like the project data, but each resource has two quota values: `quota` is the quota assigned by the
cloud-admin to the domain, and `projects_quota` is the sum of all quotas assigned to projects in that domain by the
domain-admin. If the backing service has a different idea of the quota values than Limes does, then `backend_quota`
shows the sum of all project quotas as seen by the backing service. If any of the aggregated backend quotas is
`-1`, the `backend_quota` field will contain the sum of the *finite* quota values only, and an additional key
`infinite_backend_quota` will be added. For example:

```js
// resources before aggregation
{ "quota": 10, "usage": 0 }
{ "quota":  5, "usage": 12, "backend_quota": -1 }
{ "quota":  5, "usage": 5 }

// resources after aggregation
{ "quota": 20, "usage": 17, "backend_quota": 15, "infinite_backend_quota": true }
```

In contrast to project data, `scraped_at` is replaced by `min_scraped_at` and `max_scraped_at`, which aggregate over the
`scraped_at` timestamps of all project data for that service and domain.

## GET /v1/clusters
## GET /v1/clusters/:cluster\_id
## GET /v1/clusters/current

Query data for clusters. Requires a cloud-admin token. Arguments:

* `service`: Limit query to resources in this service. May be given multiple times.
* `area`: Limit query to resources in services in this area. May be given multiple times.
* `resource`: When combined, with `?service=`, limit query to that resource.
* `local`: When given, quota and usage for shared resources is not aggregated across clusters (see below).

Returns 200 (OK) on success. Result is a JSON document like:

```json
{
  "current_cluster": "example-cluster-2",
  "clusters": [
    {
      "id": "example-cluster",
      "services": [
        {
          "type": "compute",
          "resources": [
            {
              "name": "instances",
              "domains_quota": 20,
              "usage": 1
            },
            {
              "name": "cores",
              "capacity": 1000,
              "domains_quota": 100,
              "usage": 2
            },
            {
              "name": "ram",
              "unit": "MiB",
              "capacity": 1048576,
              "domains_quota": 204800,
              "usage": 2048
            }
          ],
          "max_scraped_at": 1486738599,
          "min_scraped_at": 1486728599
        },
        {
          "type": "object-store",
          "shared": true,
          "resources": [
            {
              "name": "capacity",
              "unit": "B",
              "capacity": 60000000000000,
              "comment": "looked it up in `df`",
              "domains_quota": 107374182400,
              "usage": 104857600
            }
          ],
          "max_scraped_at": 1486733778,
          "min_scraped_at": 1486723778
        },
        ...
      ],
      "max_scraped_at": 1486712957,
      "min_scraped_at": 1486701582
    },
    ...
  ]
}
```

The `current_cluster` key is only present if no `:cluster_id` was given.

If `:cluster_id` was given, the outer key is `cluster` and its value is the object without the array surrounding it. As
a special case, a cluster ID of `current` will be substituted by the current cluster (i.e. the one for which domains and
projects can be inspected on this endpoint).

Clusters do not have a quota, but resources may be constrained by a `capacity` value. The `domains_quota` field behaves
just like the `projects_quota` key on domain level. Discrepancies between project quotas in Limes and in backing
services will not be shown on this level, so there is no `backend_quota` key.

The `capacity` key is will only be supplied when a capacity is known. Capacity values can be maintained by cluster
administrators, in which case a `comment` string will be present (such as for `object_storage/capacity` in the example
output above). The capacity is only informational: Cloud admins can choose to exceed the reported capacity when
allocating quota to domains.

The `min_scraped_at` and `max_scraped_at` timestamps on the service level refer to the usage values (aggregated over all
projects just like for `GET /domains`).

The `min_scraped_at` and `max_scraped_at` timestamps on the cluster level refer to the cluster capacity values. Capacity
plugins (and thus, capacity scraping events) are not bound to a single service, which is why the scraping timestamps
cannot be shown on the service level here.

For resources belonging to a cluster-local service (the default), the reported quota and usage is aggregated only over
domains in this cluster. For resources belonging to a shared service, the reported quota and usage is aggregated over
all domains in all clusters (and will thus be the same for every cluster listed), unless the query parameter `local` is
given. Shared services are indicated by the `shared` key on the service level (which defaults to `false` if not
specified).

## GET /v1/inconsistencies

Requires a cloud-admin token. Detects inconsistent quota setups for domains and projects in this cluster. The following
situations are considered:

1. `domain_quota_overcommitted` &ndash; The total quota of some resource across all projects in some domain exceeds the
   quota of that resource for the domain. (In other words, the domain has distributed more quota to its projects than it
   has been given.) This may happen when new projects are created and their quota is initialized because of constraints
   configured by a cloud administrator.
2. `project_quota_overspend` &ndash; The quota of some resource in some project is lower than the usage of that resource
   in that project. This may happen when someone else changes the quota in the backend service directly and increases
   usage before Limes intervenes, or when a cloud administrator changes quota constraints.
3. `project_quota_mismatch` &ndash; The quota of some resource in some project differs from the backend quota for that
   resource and project. This may happen when Limes is unable to write a changed quota value into the backend, for
   example because of a service downtime.

Accepts the arguments `service`, `area` and `resource` with the same filtering semantics as for other GET endpoints (see
above). Returns 200 (OK) on success. Result is a JSON document like:

```json
{
  "domain_quota_overcommitted": [
    {
      "domain": {
        "id": "d5fbe312-1f48-42ef-a36e-484659784aa0",
        "name": "example-domain"
      },
      "service": "network",
      "resource": "security_groups",
      "domain_quota": 100,
      "projects_quota": 114
    },
    ...
  ],
  "project_quota_overspend": [
    {
      "project": {
        "id": "8ad3bf54-2401-435e-88ad-e80fbf984c19",
        "name": "example-project",
        "domain": {
          "id": "d5fbe312-1f48-42ef-a36e-484659784aa0",
          "name": "example-domain"
        }
      },
      "service": "network",
      "resource": "routers",
      "quota": 5,
      "usage": 6
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
      "service": "network",
      "resource": "routers",
      "quota": 5,
      "backend_quota": 7
    },
    ...
  ]
}
```

Each entry in those three lists concerns exactly one resource in one project or domain. If multiple resources in the
same project are inconsistent, they will appear as multiple entries. Like in the example above, the same project and
resource may appear both in `project_quota_overspend` and `project_quota_mismatch` if `quota < usage < backend_quota`.

## POST /v1/domains/discover

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

## POST /v1/domains/:domain\_id/projects/discover

Requires a domain-admin token for the specified domain. Queries Keystone in order to discover newly-created projects in
this domain that Limes does not yet know about. This works exactly like `POST /domains/discover`, except that the JSON
document will list `new_projects` instead of `new_domains`.

*Rationale:* Same as for domain discovery: The domain admin might want to assign quotas immediately after creating a new
project.

## POST /v1/domains/:domain\_id/projects/:project\_id/sync

Requires a project-admin token for the specified project. Schedules a sync job that pulls quota and usage data for this
project from the backing services into Limes' local database. When the job was scheduled successfully, returns 202
(Accepted).

If the project does not exist in Limes' database yet, query Keystone to see if this project was just created. If so, create the project in Limes' database before returning 202 (Accepted).

*Rationale:* When a project administrator wants to adjust her project's quotas, she might discover that the usage data
shown by Limes is out-of-date. She can then use this call to refresh the usage data in order to make a more informed
decision about how to adjust her quotas.

## PUT /v1/domains/:domain\_id

Set quotas for the given domain. Requires a cloud-admin token, and a request body that is a JSON document like:

```json
{
  "domain": {
    "services": [
      {
        "type": "compute",
        "resources": [
          {
            "name": "instances",
            "quota": 30
          },
          {
            "name": "cores",
            "quota": 150
          }
        ]
      },
      {
        "type": "object-store",
        "resources": [
          {
            "name": "capacity",
            "quota": 60000,
            "unit": "MiB"
          }
        ]
      }
    ]
  }
}
```

For resources that are measured rather than counted, the values are interpreted with the same unit that is mentioned for
this resource in `GET /domains/:domain_id`. However, a `unit` string may be given to override this default. All
resources that are not mentioned in the request body remain unchanged. This operation will not affect any project
quotas in this domain.

Returns 200 (OK) on success, with a response body identical to `GET` on the same URL, containing the updated quota
values.

## PUT /v1/domains/:domain\_id/projects/:project\_id

Set quotas for the given project. Requires a domain-admin token for the specified domain. Other than that, the call
works in the same way as `PUT /domains/:domain_id`.

## PUT /v1/clusters/:cluster_id

## PUT /v1/clusters/current

Set capacity values for the given cluster. Requires a cloud-admin token, and a request body that is a JSON document
like:

```json
{
  "cluster": {
    "services": [
      {
        "type": "compute",
        "resources": [
          {
            "name": "instances",
            "capacity": 30,
            "comment": "guesstimate"
          },
          {
            "name": "cores",
            "capacity": 150,
            "comment": "counted them by hand"
          }
        ]
      },
      {
        "type": "object-store",
        "resources": [
          {
            "name": "capacity",
            "capacity": 0,
            "comment": "data center on fire"
          }
        ]
      }
    ]
  }
}
```

For resources that are measured rather than counted, the values are interpreted with the same unit that is mentioned for
this resource in `GET /clusters/:cluster_id`. However, a `unit` string may be given to override this default. All
resources that are not mentioned in the request body remain unchanged. This operation will not affect any domain or
project quotas.

Capacity values can only be set for resources which Limes does not know how to measure automatically. A `comment` is
always required, and should ideally contain a description of how the capacity value was derived. An existing
capacity value can be deleted by setting it to `-1`, in which case no `comment` is required.

Returns 200 (OK) on success, with a response body identical to `GET` on the same URL, containing the updated capacity
values.
