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

| Field | Default | Description |
| --- | --- | --- |
| `database.location` | *Required* | A [libpq connection URI][pq-uri] that locates the Limes database. The non-URI "connection string" format is not allowed; it must be a URI. |
| `database.migrations` | *Required* | Path to the directory containing the migration files for Limes' database schema. These are usually installed in `/usr/share/limes/migrations`. In development setups, point this to the directory `$repo_root/pkg/db/migrations`. |

[yaml]:   http://yaml.org/
[pq-uri]: https://www.postgresql.org/docs/9.6/static/libpq-connect.html#LIBPQ-CONNSTRING
