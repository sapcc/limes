# Configuration options

Limes accepts configuration options via environment variables for some components and
requires a configuration file ([see below](#configuration-file)) for cluster options in the [YAML format][yaml].

Use the table of contents icon
<img src="https://github.com/github/docs/raw/main/contributing/images/table-of-contents.png" width="25" height="25" />
on the top left corner of this document to get to a specific section of this guide quickly.

## Common environment variables

| Variable | Default | Description |
| --- | --- | --- |
| `LIMES_DB_NAME` | `limes` | The name of the database. |
| `LIMES_DB_USERNAME` | `postgres` | Username of the user that Limes should use to connect to the database. |
| `LIMES_DB_PASSWORD` | *(optional)* | Password for the specified user. |
| `LIMES_DB_HOSTNAME` | `localhost` | Hostname of the database server. |
| `LIMES_DB_PORT` | `5432` | Port on which the PostgreSQL service is running on. |
| `LIMES_DB_CONNECTION_OPTIONS` | *(optional)* | Database connection options. |
| `OS_...` | *(required)* | A full set of OpenStack auth environment variables for Limes's service user. See [the documentation of the NewProviderClient function](https://pkg.go.dev/github.com/sapcc/go-bits/gophercloudext#NewProviderClient) for which variables are allowed. |

## Environment variables for `limes serve` only

| Variable | Default | Description |
| --- | --- | --- |
| `LIMES_API_LISTEN_ADDRESS` | `:80` | Bind address for the HTTP API exposed by this service, e.g. `127.0.0.1:80` to bind only on one IP, or `:80` to bind on all interfaces and addresses. |
| `LIMES_API_POLICY_PATH` | `/etc/limes/policy.yaml` | Path to the oslo.policy file that describes authorization behavior for this service. Please refer to the [OpenStack documentation on policies][policy] for syntax reference. This repository includes an [example policy][ex-pol] that can be used for development setups, or as a basis for writing your own policy. For `:set_rate_limit` policies, the object attribute `%(service_type)s` is available to restrict editing to certain service types. |

### Audit trail

The Limes API logs all write operations at the domain and project level in the Open Standards [CADF format](https://www.dmtf.org/standards/cadf).
These audit events can be sent to a RabbitMQ server which can then forward them to any cloud audit API, datastore, etc.

| Variable | Default | Description |
| --- | --- | --- |
| `LIMES_AUDIT_RABBITMQ_QUEUE_NAME` | *(optional)* | Name for the queue that will hold the audit events. The events are published to the default exchange. If not given, audit logging is disabled and audit events will only be written to the debug log. |
| `LIMES_AUDIT_RABBITMQ_USERNAME` | `guest` | RabbitMQ Username. |
| `LIMES_AUDIT_RABBITMQ_PASSWORD` | `guest` | Password for the specified user. |
| `LIMES_AUDIT_RABBITMQ_HOSTNAME` | `localhost` | Hostname of the RabbitMQ server. |
| `LIMES_AUDIT_RABBITMQ_PORT` | `5672` | Port number to which the underlying connection is made. |

## Environment variables for `limes collect` only

| Variable | Default | Description |
| --- | --- | --- |
| `LIMES_AUTHORITATIVE` | *(required)* | If set to `true`, the collector will write the quota from its own database into the backend service whenever scraping encounters a backend quota that differs from the expectation. This flag is strongly recommended in production systems to avoid divergence of Limes quotas from backend quotas, but should be used with care during development. |
| `LIMES_COLLECTOR_METRICS_LISTEN_ADDRESS` | `:8080` | Bind address for the Prometheus metrics endpoint provided by this service. See `LIMES_API_LISTEN_ADDRESS` for acceptable values. |
| `LIMES_COLLECTOR_DATA_METRICS_EXPOSE` | `false` | If set to `true`, expose all quota/usage/capacity data as Prometheus gauges. This is disabled by default because this can be a lot of data for OpenStack clusters containing many projects, domains and services. |
| `LIMES_COLLECTOR_DATA_METRICS_SKIP_ZERO` | `false` | If set to `true`, data metrics will only be emitted for non-zero values. In large deployments, this can substantially reduce the amount of timeseries emitted. |
| `LIMES_QUOTA_OVERRIDES_PATH` | *(optional)* | Path to a JSON file containing the quota overrides for this cluster. |

If present, the quota overrides file must be a four-leveled object, with the keys being domain name, project name,
service type and resource name in that order. If API-level resource renaming is used (see configuration option
`resource_behavior[].identity_in_v1_api`), the service type and resource name refer to the renamed identifiers used by
the v1 API. The values are either numbers (to override quotas on counted resources) or strings of the form
`<number> <unit>` (to override quotas on measured resources). For example:

```json
{
  "domain-one": {
    "project-one": {
      "compute": {
        "cores": 10000,
        "ram": "1 TiB"
      }
    },
    "project-two": {
      "object-store": {
        "capacity": "0 B"
      },
      "keppel": {
        "images": 0
      }
    }
  },
  "domain-two": {
    "project-three": {
      "compute": {
        "cores": 50000,
        "ram": "512 GiB"
      }
    }
  }
}
```

## Environment variables for `limes serve-data-metrics` only

| Variable | Default | Description |
| --- | --- | --- |
| `LIMES_DATA_METRICS_LISTEN_ADDRESS` | `:8080` | Bind address for the Prometheus metrics endpoint provided by this service. See `LIMES_API_LISTEN_ADDRESS` for acceptable values. |
| `LIMES_DATA_METRICS_SKIP_ZERO` | `false` | If set to `true`, data metrics will only be emitted for non-zero values. In large deployments, this can substantially reduce the amount of timeseries emitted, at the expense of making some PromQL queries harder to formulate. |

## Configuration file

A configuration file in YAML format must be provided that describes things like the set of available backend services and the quota/capacity scraping behavior. A minimal config file could look like this:

```yaml
availability_zones:
  - east-1
  - west-1
  - west-2
services:
  - type: compute
  - type: network
capacitors:
  - id: nova
    type: nova
```

The following fields and sections are supported:

| Field | Required | Description |
| --- | --- | --- |
| `availability_zones` | yes | List of availability zones in this cluster. |
| `catalog_url` | no | URL of Limes API service as it appears in the Keystone service catalog for this cluster. This is only used for version advertisements, and can be omitted if no client relies on the URLs in these version advertisements. |
| `discovery.method` | no | Defines which method to use to discover Keystone domains and projects in this cluster. If not given, the default value is `list`. |
| `discovery.except_domains` | no | May contain a regex. Domains whose names match the regex will not be considered by Limes. |
| `discovery.only_domains` | no | May contain a regex. If given, only domains whose names match the regex will be considered by Limes. If `except_domains` is also given, it takes precedence over `only_domains`. |
| `discovery.params` | yes/no | A subsection containing additional parameters for the specific discovery method. Whether this is required depends on the discovery method; see [*Supported discovery methods*](#supported-discovery-methods) for details. |
| `services` | yes | List of backend services for which to scrape quota/usage data. Service types for which Limes does not include a suitable *quota plugin* will be ignored. See below for supported service types. |
| `capacitors` | no | List of capacity plugins to use for scraping capacity data. See below for supported capacity plugins. |
| `resource_behavior` | no | Configuration options for special resource behaviors. See [*resource behavior*](#resource-behavior) for details. |
| `quota_distribution_configs` | no | Configuration options for selecting resource-specific quota distribution models. See [*quota distribution models*](#quota-distribution-models) for details. |

### Resource behavior

Some special behaviors for resources can be configured in the `resource_behavior[]` section. Each entry in this section can match multiple resources.

| Field | Required | Description |
| --- | --- | --- |
| `resource_behavior[].resource` | yes | Must contain a regex. The behavior entry applies to all resources where this regex matches against a slash-concatenated pair of service type and resource name. The anchors `^` and `$` are implied at both ends, so the regex must match the entire phrase. |
| `resource_behavior[].overcommit_factor` | no | If given, capacity for matching resources will be computed as `raw_capacity * overcommit_factor`, where `raw_capacity` is what the capacity plugin reports. |
| `resource_behavior[].commitment_durations` | no | If given, commitments for this resource can be created with any of the given durations. The duration format is the same as in the `commitments[].duration` attribute that appears on the resource API. If empty, this resource does not accept commitments. |
| `resource_behavior[].commitment_min_confirm_date` | no | If given, commitments for this resource will always be created with `confirm_by` no earlier than this timestamp. This can be used to plan the introduction of commitments on a specific date. Ignored if `commitment_durations` is empty. |
| `resource_behavior[].commitment_until_percent` | no | If given, commitments for this resource will only be confirmed while the total of all confirmed commitments or uncommitted usage in the respective AZ is smaller than the respective percentage of the total capacity for that AZ. This is intended to provide a reserved buffer for the growth quota configured by `quota_distribution_configs[].autogrow.growth_multiplier`. Defaults to 100, i.e. all capacity is committable. |
| `resource_behavior[].commitment_conversion.identifier` | no | If given, must contain a string. Commitments for this resource will then be allowed to be converted into commitments for all resources that set the same conversion identifier. |
| `resource_behavior[].commitment_conversion.weight` | no | If given, must contain an integer. When converting commitments for this resource into another compatible resource, the ratio of the weights of both resources gives the conversion rate for the commitment amount. For example, if resource `foo` has a weight of 2 and `bar` has a weight of 5, the conversion rate is 2:5, meaning that a commitment for 10 units of `foo` would be converted into a commitment for 25 units of `bar`. |
| `resource_behavior[].identity_in_v1_api` | no | If given, must be a slash-concatenated pair of service type and resource name, e.g. `myservice/someresource`. The resource will appear as having the specified name and occurring within the specified service when queried on the v1 API. See [*Resource renaming*](#resource-renaming) for details. |
| `resource_behavior[].category` | no | If given, matching resources belong to the given category. This is a UI hint to subdivide resources within the same service into logical groupings. |

For example:

```yaml
resource_behavior:
  # matches both sharev2/share_capacity and sharev2/snapshot_capacity
  - { resource: sharev2/.*_capacity, overcommit_factor: 2 }
  # starting in 2024, offer commitments for Cinder storage
  - { resource: volumev2/capacity, commitment_durations: [ 1 year, 2 years, 3 years ], commitment_min_confirm_date: 2024-01-01T00:00:00Z }
  # an Ironic flavor has been renamed from "thebigbox" to "baremetal_large"
  - { resource: compute/instances_baremetal_large, identity_in_v1_api: compute/instances_thebigbox }
```

The fields `category` and `identity_in_v1_api` can be templated with placeholders `$1`, `$2`, etc., which will be filled with the respective match groups from the `resource` regex. For example:

```yaml
resource_behavior:
  # this entire service was renamed
  - { resource: 'volumev2/(.*)', identity_in_v1_api: 'cinder/$1' }
  # this service contains sets of resources for each share type
  - { resource: 'manila/(shares|snapshots|share_capacity|snapshot_capacity)_(.+)', category: 'share_type_$2' }
```

#### Resource renaming

Limes provides amenities for renaming and restructuring services and resources in a way where the backwards-incompatible
change to these identifiers can take place internally, while happening at a separate later date in the API. For example,
suppose that in the service type `compute`, the resource `instances_thebigbox` models separate instance quota for the
baremetal flavor `thebigbox`. Suppose further that it is later decided to change this rather silly name to a more
conventional name like `baremetal_large`. The resource therefore has to be renamed to `instances_baremetal_large`, but
this might break users of the Limes API that rely on the existing resource name. The following configuration can be
applied to mask the internal name change from API users until an announcement can be made to switch over to the new name
on the API at a later date:

```yaml
resource_behavior:
  - { resource: compute/instances_baremetal_large, identity_in_v1_api: compute/instances_thebigbox }
```

On the subject of resource renaming, here is a playbook-level explanation of how to actually rename resources like this
within the same service: (Replace service types and resource names as necessary.)

1. Stop all processes that might write into the Limes DB (specifically, `limes serve` and `limes collect`) to avoid
   interference in the next step.
2. Update the `name` columns in the resources tables of the DB:
   ```sql
   UPDATE cluster_resources SET name = 'instances_baremetal_large' WHERE name = 'instances_thebigbox'
      AND service_id IN (SELECT id FROM cluster_services WHERE type = 'compute');
   UPDATE project_resources SET name = 'instances_baremetal_large' WHERE name = 'instances_thebigbox'
      AND service_id IN (SELECT id FROM project_services WHERE type = 'compute');
   ```
3. Apply configuration matching the new resource name to all relevant processes (specifically, the Limes core components
   and the respective liquid).
4. Restart all relevant processes.

The process is slightly more involved when the renaming moves resources to a different service. For example, suppose
that we have all network-related resources grouped under service type `network` (as was the case before Octavia was
split from Neutron)  and we want to split them into the new service types `neutron` and `octavia`. Specifically, the
resource `network/routers` needs to move to `neutron/routers`. In this case, replace step 2 from the example playbook
with the following sequence:

1. If not done yet, ensure that service records exist for the target service type:
   ```sql
   INSERT INTO cluster_services (type) VALUES ('neutron')
       ON CONFLICT DO NOTHING;
   INSERT INTO project_services (project_id, type, next_scrape_at, rates_next_scrape_at)
       SELECT id, 'neutron', NOW(), NOW() FROM projects
       ON CONFLICT DO NOTHING;
   ```
2. Attach the existing resource records to the new service records:
   ```sql
   UPDATE cluster_resources
       SET service_id = (SELECT id FROM cluster_services WHERE type = 'neutron')
       WHERE name = 'routers' AND service_id = (SELECT id FROM cluster_services WHERE type = 'network');

   UPDATE project_resources res SET service_id = (
       SELECT new.id FROM project_services new JOIN project_services old ON old.project_id = new.project_id
       WHERE old.id = res.service_id AND old.type = 'network' AND new.type = 'neutron'
   ) WHERE name = 'routers' AND service_id IN (SELECT id FROM project_services WHERE type = 'network');
   ```
   If the resource name also changes, add `SET name = 'newname'` to each UPDATE statement.
   If multiple resources need to be moved from the same old service type to the same new service type, you can replace
   `WHERE name = 'routers'` by a list match, e.g. `WHERE name IN ('routers','floating_ips','networks')`.
3. If this was the last resource in the old service type, clean up the old service type:
   ```sql
   DELETE FROM cluster_services WHERE type = 'network' AND id NOT IN (SELECT DISTINCT service_id FROM cluster_resources);
   DELETE FROM project_services WHERE type = 'network' AND id NOT IN (SELECT DISTINCT service_id FROM project_resources);
   ```

Alternatively, if an entire service is renamed without changing its resource structure, then the following simplified
database update process can be substituted instead: (This example renames service `object-store` to `swift`.)

1. Update the `type` columns in the services tables of the DB:
   ```sql
   UPDATE cluster_services SET type = 'swift' WHERE type = 'object-store';
   UPDATE project_services SET type = 'swift' WHERE type = 'object-store';
   ```

### Quota distribution models

Each resource uses one of several quota distribution models, with `autogrow` being the default.

Resource-specific distribution models can be configured per resource in the `quota_distribution_configs[]` section.
Each entry in this section can match multiple resources.

| Field | Required | Description |
| --- | --- | --- |
| `quota_distribution_configs[].resource` | yes | Must contain a regex. The config entry applies to all resources where this regex matches against a slash-concatenated pair of service type and resource name. The anchors `^` and `$` are implied at both ends, so the regex must match the entire phrase. |
| `quota_distribution_configs[].model` | yes | As listed below. |

A distribution model config can contain additional options based on which model is chosen (see below).

#### Model: `autogrow` (default)

In this model, quota is automatically distributed ("auto") such that:

1. all active commitments and usage are represented in their respective project quota, and
2. there is some space beyond the current commitment/usage ("grow"), defined through a growth multiplier.
   For example, a growth multiplier of 1.2 represents 20% quota on top of the current commitment and/or usage.

Domain quota is irrelevant under this model. Project quota is calculated for each AZ resource along the following guidelines:

```
hard_minimum_quota = max(confirmed_commitments.sum(), current_usage)
soft_minimum_quota = max(hard_minimum_quota, historical_usage.max())
desired_quota      = max(confirmed_commitments.sum(), historical_usage.min()) * growth_multiplier
```

All projects first get their hard minimum. Then, remaining capacity is distributed as quota, initially to satisfy the
soft minimums, and then to try and reach the desired quota.

As an additional constraint, if the resource defines a **base quota**, additional quota will be granted in the pseudo-AZ
`any` to ensure that the total quota over all AZs is equal to the base quota. A nonzero base quota must be defined for
all resources that new projects shall be able to use without having to create commitments.

**Historical usage** refers to the project's usage over time, within the constraint of the configured retention period
(see below). This is used to limit the speed of growth: If only current usage were considered, the assigned quota would
rise pretty much instantly after usage increases. But then quota would not really pose any boundary at all. (If this is
desired, a very short retention period like `1m` can be configured.) By considering historical usage over e.g. the last
two days (retention period `48h`), quota will never grow by more than one growth multiplier per two days from usage
alone. (Larger quota jumps are still possible if additional commitments are confirmed.)

Autogrow requires an object with configuration options at `quota_distribution_configs[].autogrow`:

| Field | Default | Description |
| --- | --- | --- |
| `allow_quota_overcommit_until_allocated_percent` | `0` | If quota overcommit is allowed, means that the desired quota and base quota is always given out to all projects, even if the sum of all project quotas ends up being greater than the resource's capacity. If this value is set to 0, quota overcommit is never allowed, i.e. the sum of all project quotas will never exceed the resource's capacity. If overcommit is to be allowed, a typical setting is something like 95% or 99%: Once usage reaches that threshold, quota overcommit will be disabled to ensure that confirmed commitments are honored. To enable quota overcommit unconditionally, set a very large value like 10000%. |
| `project_base_quota` | `0` | The minimum amount of quota that will always be given to a project even if it does not have that much commitments or usage to warrant it under the regular formula. Can be set to zero to force projects to bootstrap their quota via commitments. |
| `growth_multiplier` | *(required)* | As explained above. Cannot be set to less than 1 (100%). Can be set to exactly 1 to ensure that no additional quota will be granted above existing usage and/or confirmed commitments. |
| `growth_minimum` | `1` | When multiplying a growth baseline greater than 0 with a growth multiplier greater than 1, ensure that the result is at least this much higher than the baseline. |
| `usage_data_retention_period` | *(required)* | As explained above. Must be formatted as a string that [`time.ParseDuration`](https://pkg.go.dev/time#ParseDuration) understands. Cannot be set to zero. To only use current usage when calculating quota, set this to a very short interval like `1m`. |

The default config for resources without a specific `quota_distribution_configs[]` match sets the default values as explained above, and also

```
growth_multiplier = 1.0
growth_minimum = 0
usage_data_retention_period = 1s
```

This default configuration means that no quota will be assigned except to cover existing usage and honor explicit quota overrides.

## Supported discovery methods

This section lists all supported discovery methods for Keystone domains and projects.

### Method: `list` (default)

```yaml
discovery:
  method: list
```

When this method is configured, Limes will simply list all Keystone domains and projects with the standard API calls, equivalent to what the CLI commands `openstack domain list` and `openstack project list --domain $DOMAIN_ID` do.

### Method: `static`

```yaml
discovery:
  method: static
  params:
    domains:
    - id: 455080d9-6699-4f46-a755-2b2f8459c147
      name: Default
      projects:
      - id: 98c34016-ea71-4b41-bb04-2e52209453d1
        name: admin
        parent_id: 455080d9-6699-4f46-a755-2b2f8459c147
```

When this method is configured, Limes will not talk to Keystone and instead just assume that exactly those domains and projects exist which are specified in the `discovery.params` config section, like in the example shown above. This method is not useful for most deployments, but can be helpful in case of migrations.

## Supported service types

This section lists all supported service types and the resources that are understood for each service. The `type` string is always equal to the one that appears in the Keystone service catalog.

### `compute`: Nova v2

```yaml
services:
  - type: compute
    service_type: compute
```

The area for this service is `compute`.

| Resource | Unit |
| --- | --- |
| `cores` | countable, AZ-aware |
| `instances` | countable, AZ-aware |
| `ram` | MiB, AZ-aware |
| `server_groups` | countable |
| `server_group_members` | countable |

#### Instance subresources

```yaml
services:
  - type: compute
    service_type: compute
    params:
      with_subresources: true
```

The `instances` resource supports subresource scraping. If enabled, subresources bear the following attributes:

| Attribute | Type | Comment |
| --- | --- | --- |
| `id` | string | instance UUID |
| `name` | string | instance name |
| `status` | string | instance status [as reported by OpenStack Nova](https://wiki.openstack.org/wiki/VMState) |
| `flavor` | string | flavor name (not ID!) |
| `ram` | integer value with unit | amount of memory configured in flavor |
| `vcpu` | integer | number of vCPUs configured in flavor |
| `disk` | integer value with unit | root disk size configured in flavor |
| `os_type` | string | identifier for OS type as configured in image (see below) |
| `hypervisor` | string | identifier for the hypervisor type of this instance (only if configured, see below) |
| `metadata` | object of strings | user-supplied key-value data for this instance as reported by OpenStack Nova |
| `tags` | array of strings | user-supplied tags for this instance as reported by OpenStack Nova |

The `os_type` field contains:
- for VMware images: the value of the `vmware_ostype` property of the instance's image, or
- otherwise: the part after the colon of a tag starting with `ostype:`, e.g. `rhel` if there is a tag `ostype:rhel` on the image.

The value of the `hypervisor` field is copied from `params.hypervisor_type` of the service configuration, or otherwise left empty.

#### Separate instance quotas

On SAP Converged Cloud (or any other OpenStack cluster where Nova carries the relevant patches), there will be an
additional resource `instances_<flavorname>` for each flavor with the `quota:separate = true` extra spec. These resources
behave like the `instances` resource. When subresources are scraped for the `instances` resource, they will also be
scraped for these flavor-specific instance resources. The flavor-specific instance resources are in the `per_flavor`
category (unless overridden by [resource behavior](#resource-behavior)).

```yaml
services:
  - type: compute
    service_type: compute
    params:
      separate_instance_quotas:
        flavor_name_selection: ^bigvm_
```

Sometimes Tempest creates resource classes or flavors that Limes recognizes as requiring a separate instance quota,
which may not be desired. To control which flavors get a separate instance quota, give the
`params.separate_instance_quotas.flavor_name_pattern` option as shown above. Only flavors with a name matching this
regex will be considered. Omit this option to match all flavors.

#### Hardware-versioned quota

On SAP Converged Cloud (or any other OpenStack cluster where Nova carries the relevant patches), there will be
additional resources following the name pattern `hw_version_(.+)_(cores|instances|ram)`. Any of these resources that
exist will also be reported by Limes. (Not all resources may exist for a given hardware version). They behave like the
regular `cores`/`instances`/`ram` resources and cover instances whose flavors have the respective value in the
`quota:hw_version` extra spec.

### `liquid`: Any service with LIQUID support

```yaml
services:
  - type: liquid
    service_type: someservice
    params:
      area: storage
      liquid_service_type: liquid-myservice
```

This is a generic integration method for any service that supports [LIQUID](https://pkg.go.dev/github.com/sapcc/go-api-declarations/liquid);
see documentation over there. The LIQUID endpoint will by located in the Keystone service catalog at service type `liquid-$SERVICE_TYPE`,
unless this default is overridden by `params.liquid_service_type`. The area for this service is as configured in `params.area`.

Currently, any increase in the ServiceInfo version of the liquid will prompt a fatal error in Limes, thus usually forcing it to restart.
This is something that we plan on changing into a graceful reload in the future.

For information on liquids provided by Limes itself, please refer to the [liquids documentation](../liquids/index.md).

## Available capacity plugins

Note that capacity for a resource only becomes visible when the corresponding service is enabled in the
`services` list as well.

Capacity plugins have a `type` (as specified in the subheadings for this section) and an `id`. For most capacity
plugins, having more than one does not make sense because they refer to other service's APIs (which only exist once per
cluster anyway). In these cases, `id` is conventionally set to the same as `type`. For capacity plugins like
`prometheus` or `manual`, it's possible to have multiple plugins of the same type at once. In that case, the `id` must
be unique for each plugin.

### `liquid`

```yaml
capacitors:
  - id: liquid-nova
    type: liquid
    params:
      service_type: compute
      liquid_service_type: liquid-nova
```

This is a generic integration method for any service that supports [LIQUID](https://pkg.go.dev/github.com/sapcc/go-api-declarations/liquid);
see documentation over there. The LIQUID endpoint will by located in the Keystone service catalog at service type `liquid-$SERVICE_TYPE`,
using the value from `params.service_type`, unless this default logic is overridden by `params.liquid_service_type`.

Currently, any increase in the ServiceInfo version of the liquid will prompt a fatal error in Limes, thus usually forcing it to restart.
This is something that we plan on changing into a graceful reload in the future.

For information on liquids provided by Limes itself, please refer to the [liquids documentation](../liquids/index.md).

### `manual`

```yaml
capacitors:
  - id: manual
    type: manual-network
    params:
      values:
        network:
          floating_ips: 8192
          networks: 4096
```

The `manual` capacity plugin does not query any backend service for capacity data. It just reports the capacity data
that is provided in the configuration file in the `params.values` key. Values are grouped by service, then by resource.

This is useful for capacities that cannot be queried automatically, but can be inferred from domain knowledge. Limes
also allows to configure such capacities via the API, but operators might prefer the `manual` capacity plugin because it
allows to track capacity values along with other configuration in a Git repository or similar.

### `nova`

This capacitor enumerates matching Nova hypervisors and reports their total capacity. It has various modes of operation.

#### Option 1: Pooled capacity

```yaml
capacitors:
  - id: nova
    type: nova
    params:
      pooled_cores_resource: cores
      pooled_instances_resource: instances
      pooled_ram_resource: ram
      hypervisor_selection:
        aggregate_name_pattern: '^(?:vc-|qemu-)'
        hypervisor_type_pattern: '^(?:VMware|QEMU)'
      flavor_selection:
        required_extra_specs:
          first: 'foo'
        excluded_extra_specs:
          second: 'bar'
      with_subcapacities: true
```

In this most common mode of operation, capacity is summed up into pooled resources:

| Resource | Method |
| --- | --- |
| `compute/${params.pooled_cores_resource}` | The sum of the reported CPUs for all hypervisors in matching aggregates. Note that the hypervisor statistics reported by Nova do not take overcommit into account, so you may have to configure the overcommitment again in Limes for accurate capacity reporting. |
| `compute/${params.pooled_instances_resource}` | Estimated as `maxInstancesPerAggregate * count(matchingAggregates)`, but never more than `sumLocalDisk / maxDisk`, where `sumLocalDisk` is the sum of the local disk size for all hypervisors, and `maxDisk` is the largest disk requirement of all matching flavors. |
| `compute/${params.pooled_ram_resource}` | The sum of the reported RAM for all hypervisors. |

Only those hypervisors are considered that belong to an aggregate whose name matches the regex in the
`params.hypervisor_selection.aggregate_name_pattern` parameter. There must be a 1:1 relation between matching aggregates
and hypervisors: If a hypervisor belongs to more than one matching aggregate, an error is raised. The recommended
configuration is to use AZ-wide aggregates here.

If the `params.hypervisor_selection.hypervisor_type_pattern` parameter is set, only those hypervisors are considered whose `hypervisor_type`
matches this regex. Note that this is distinct from the `hypervisor_type_rules` used by the `compute` quota plugin, and
uses the `hypervisor_type` reported by Nova instead.

The `params.hypervisor_selection` can also contains lists of strings in the `required_traits` and `shadowing_traits` keys.

- If `required_traits` is given, it must contain a list of trait names, each optionally prefixed with `!`. Only those
  hypervisors will be considered whose resource providers have all of the traits without `!` prefix and none of those
  with `!` prefix.
- If `shadowing_traits` is given, it must have the same format as described above for `required_traits`. If a hypervisor
  matches any of the rules in this configuration field (using the same logic as above for `required_traits`), the
  hypervisor will be considered shadowed. Its capacity will not be counted, just as for hypervisors that are excluded
  entirely by `required_traits`, but if the hypervisor contains running instances of split flavors (see below), this
  existing usage will be counted towards the total capacity. Shadowing is used to represent usage by legacy instances
  while migrating to a different resource provider trait setup. It can also be employed to report capacity
  conservatively during a decommissioning operation: When moving payload from an old hypervisor to a newer model, both
  hypervisors should be shadowed during this operation to avoid higher capacity being reported while both generations of
  hypervisor are enabled concurrently.

The `params.flavor_selection` parameter can be used to control how flavors are enumerated. Only those flavors will be
considered which have all the extra specs noted in `required_extra_specs`, and none of those noted in
`excluded_extra_specs`. In the example, only flavors will be considered that have the extra spec "first" with the value
"foo", and which do not have the value "bar" in the extra spec "second".
This is particularly useful to filter Ironic flavors, which usually have much larger root disk sizes.

When subcapacity scraping is enabled (as shown above), subcapacities will be scraped for all three resources. Each
subcapacity corresponds to one Nova hypervisor. If the `params.hypervisor_type_pattern` parameter is set, only matching
hypervisors will be shown. Aggregates with no matching hypervisor will not be considered. Subcapacities bear the
following attributes:

| Attribute | Type | Comment |
| --- | --- | --- |
| `service_host` | string | `service.host` attribute of hypervisor as reported by Nova |
| `az` | string | availability zone |
| `aggregate` | string | name of aggregate matching `params.aggregate_name_pattern` that contains this hypervisor |
| `capacity` | integer | capacity of the resource in question in this hypervisor |
| `usage` | integer | usage of the resource in question in this hypervisor |
| `traits` | array of strings | traits reported by Placement on this hypervisor's resource provider |

### Option 2: Split capacity for flavors with separate instance quota

```yaml
capacitors:
  # As this example implies, there will often be multiple instances of this capacitor for different hypervisor types.
  - id: nova-binpack-flavors-type42
    type: nova
    params:
      flavor_selection:
        required_extra_specs:
          trait:CUSTOM_HYPERVISOR_TYPE_42: required
      hypervisor_selection:
        aggregate_name_pattern: '^(?:vc-|qemu-)'
        hypervisor_type_pattern: '^(?:VMware|QEMU)'
        required_traits: [ CUSTOM_HYPERVISOR_TYPE_42 ]
```

This capacity plugin can also report capacity for the special `compute/instances_<flavorname>` resources that exist on
SAP Converged Cloud ([see above](#compute-nova-v2)). Flavors with such a resource are called **split flavors** because
they do not count towards the regular `cores`, `instances` and `ram` resources, but only towards their own separate
instance quota.

Unlike `liquid-ironic`, this capacitor is used for VM flavors. Usually, the resource provider traits are used to limit
the VM flavors to certain hypervisors where no other VMs can be deployed (see below at "Option 3" for what happens if
there is no such limitation). The `flavor_selection` and `hypervisor_selection` parameters work the same as explained
above for "Option 1".

Capacity calculation is not as straight-forward as for the `nova` capacitor: Nova and Placement only tell us about the
size of the hypervisors in terms of CPU, RAM and local disk, but the capacitor wants to report a capacity in terms of
number of instances that can be deployed per flavor. There is no single correct answer to this because different flavors
use different amounts of resources.

This capacitor takes existing usage and confirmed commitments, as well as commitments that are waiting to be confirmed,
for its respective `compute/instances_<flavorname>` resources and simulates placing those existing and requested
instances onto the matching hypervisors. Afterwards, any remaining space is filled up by following the existing
distribution of flavors as closely as possible. The resulting capacity is equal to how many instances could be placed in
this simulation. The placement simulation strives for an optimal result by using a binpacking algorithm.

The placement simulation can be tweaked through the additional configuration attributes:

| Field | Type | Explanation |
| --- | --- | --- |
| `params.binpack_behavior.score_ignores_cores`<br>`params.binpack_behavior.score_ignores_disk`<br>`params.binpack_behavior.score_ignores_ram` | boolean | If true, when ranking nodes during placement, do not include the respective dimension in the score. |

Ignoring dimensions in the score is useful if there is one dimension that throws off the placement simulation. However,
if multiple dimensions are ignored, the placement algorithm deteriorates into very crude "first fit" logic.

#### Option 3: Hybrid mode

If `pooled_cores_resource` etc. are set (like in option 1), but there are also matching flavors with separate instance
quota (like in option 2), capacity will be calculated using the following hybrid algorithm:

- For each type of demand (current usage, unused commitments and pending commitments) in that order, the requisite
  amount of demanded instances is placed for each split flavor, while ensuring that enough free space remains to fulfil
  the demand of pooled resources. For example, unused commitments in split flavors can only be placed if there is still
  space left after placing the current usage in split flavors and while also blocking the current usage and unused
  commitments in pooled resources.
- When filling up the remaining space with extra split-flavor instances like at the end of Option 2, extra instances are
  only placed into the "fair share" of split flavors when compared with pooled flavors. For example, if current demand
  of CPU, RAM and disk is 10% in split flavors and 30% in pooled flavors, that's a ratio of 1:3. Therefore, split-flavor
  instances will be filled up to 25% of the total space to leave 75% for pooled flavors, thus matching this ratio.
- Finally, capacity for split flavors is calculated by counting the number of placements thus simulated, and capacity
  for pooled flavors is reported as the total capacity minus the capacity taken up by the simulated split-flavor
  instance placements.

### `prometheus`

```yaml
capacitors:
  - id: prometheus
    type: prometheus-compute
    params:
      api:
        url: https://prometheus.example.com
        cert:    /path/to/client.pem
        key:     /path/to/client-key.pem
        ca_cert: /path/to/server-ca.pem
      queries:
        compute:
          cores:     sum by (az) (hypervisor_cores)
          ram:       sum by (az) (hypervisor_ram_gigabytes) * 1024
```

Like the `manual` capacity plugin, this plugin can provide capacity values for arbitrary resources. A [Prometheus][prom]
instance must be running at the URL given in `params.api.url`. Each of the queries in `params.queries` is
executed on this Prometheus instance. Queries are grouped by service, then by resource.

Each query must result in one or more metrics with a unique `az` label. The values will be reported as capacity for that
AZ, if the AZ is one of the known AZs or the pseudo-AZ `any`. All other capacity will be grouped under the pseudo-AZ
`unknown` as a safe fallback. For non-AZ-aware resources, you can wrap the query expression in
`label_replace($QUERY, "az", "any", "", "")` to add the required label.

In `params.api`, only the `url` field is required. You can pin the server's CA
certificate (`params.api.ca_cert`) and/or specify a TLS client certificate
(`params.api.cert`) and private key (`params.api.key`) combination that
will be used by the HTTP client to make requests to the Prometheus API.

For example, the following configuration can be used with [swift-health-exporter][she] to find the net capacity of a Swift cluster with 3 replicas:

```yaml
capacitors:
  - id: prometheus
    type: prometheus-swift
    params:
      api_url: https://prometheus.example.com
      queries:
        object-store:
          capacity: min(swift_cluster_storage_capacity_bytes < inf) / 3
```

[yaml]:   http://yaml.org/
[pq-uri]: https://www.postgresql.org/docs/9.6/static/libpq-connect.html#LIBPQ-CONNSTRING
[policy]: https://docs.openstack.org/security-guide/identity/policies.html
[ex-pol]: ../example-policy.yaml
[prom]:   https://prometheus.io
[she]:    https://github.com/sapcc/swift-health-exporter

## Rate Limits

Rate limits can be configured per service on 2 levels: `global` and `project_default` as outlined below.
For further details see the [rate limits API specification](../users/api-v1-specification.md#rate-limits).

| Attribute | Type | Comment |
| --- | --- | --- |
| `global` | list |  Defines rate limits meant to protect the service from being overloaded. Requests are counted across all domains and projects. |
| `project_default` | list | Defines default rate limits for every project. |
| `$level[].name` | string | The name of the rate. |
| `$level[].unit` | string | The unit of the rate. Available units are `B`, `KiB`, `MiB` and so on. If not given, the resource is counted (e.g. API requests) rather than measured. |
| `$level[].limit` | integer | The rate limit as integer. |

Example configuration:

```yaml
services:
  - type: object-store
    rates:
      global:
       - name:   services/swift/account/container/object:create
         limit:  1000000
         window: "1s"
      project_default:
       - name:   services/swift/account/container/object:create
         limit:  1000
         window: "1s"
```
