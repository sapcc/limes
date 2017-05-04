# Configuration guide

Limes requires a configuration file in [YAML format][yaml]. A minimal complete configuration could look like this:

```yaml
database:
  location: "postgres://postgres@localhost/limes"
  migrations: "/usr/share/limes/migrations"

api:
  listen: "127.0.0.1:8080"
  policy: "/etc/limes/policy.json"

collector:
  metrics: "127.0.0.1:8081"

clusters:
  staging:
    auth_url:            https://keystone.staging.example.com/v3
    user_name:           limes
    user_domain_name:    Default
    project_name:        service
    project_domain_name: Default
    password:            swordfish
  services:
    - type: compute
    - type: network
  capacitors:
    - id: nova
```

Read on for the full list and description of all configuration options.

## Section "database"

Configuration options relating to the database connection of all services.

| Field | Default | Description |
| --- | --- | --- |
| `database.location` | *Required* | A [libpq connection URI][pq-uri] that locates the Limes database. The non-URI "connection string" format is not allowed; it must be a URI. |
| `database.migrations` | *Required* | Path to the directory containing the migration files for Limes' database schema. These are usually installed in `/usr/share/limes/migrations`. In development setups, point this to the directory `$repo_root/pkg/db/migrations`. |

## Section "api"

Configuration options relating to the behavior of the API service.

| Field | Default | Description |
| --- | --- | --- |
| `api.listen` | *Required* | Bind address for the HTTP API exposed by this service, e.g. `127.0.0.1:8080` to bind only on one IP, or `:8080` to bind on all interfaces and addresses. |
| `api.policy` | *Required* | Path to the oslo.policy file that describes authorization behavior for this service. Please refer to the [OpenStack documentation on policies][policy] for syntax reference. This repository includes an [example policy][ex-pol] that can be used for development setups, or as a basis for writing your own policy. |

## Section "collector"

Configuration options relating to the behavior of the collector service.

| Field | Default | Description |
| --- | --- | --- |
| `collector.metrics` | *Required* | Bind address for the Prometheus metrics endpoint provided by this service. See `api.listen` for acceptable values. |
| `collector.data_metrics` | `false` | If `true`, expose all quota/usage/capacity data as Prometheus gauges. This is disabled by default because this can be a lot of data for OpenStack clusters containing many projects, domains and services. |

## Section "clusters"

Configuration options describing the OpenStack clusters which Limes shall cover. `$id` is the internal *cluster ID*, which may be chosen freely, but should not be changed afterwards. (It *can* be changed, but that requires a downtime and manual editing of the database.)

| Field | Default | Description |
| --- | --- | --- |
| `clusters.$id.auth_url` | *Required* | URL for Keystone v3 API in this cluster.

[yaml]:   http://yaml.org/
[pq-uri]: https://www.postgresql.org/docs/9.6/static/libpq-connect.html#LIBPQ-CONNSTRING
[policy]: https://docs.openstack.org/security-guide/identity/policies.html
[ex-pol]: ../example-policy.json
