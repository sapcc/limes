# Configuration options

Limes accepts configuration options via environment variables for some components and
requires a configuration file ([see below](#configuration-file)) for cluster options in the [YAML format][yaml].

Use the table of contents icon
<img src="https://github.com/github/docs/raw/main/assets/images/table-of-contents.png" width="25" height="25" />
on the top left corner of this document to get to a specific section of this guide quickly.

## Common environment variables

| Variable | Default | Description |
| --- | --- | --- |
| `LIMES_CONSTRAINTS_PATH` | no | Path to a YAML file containing the quota constraints for this cluster. See [*quota constraints*](constraints.md) for details. |
| `LIMES_DB_NAME` | `limes` | The name of the database. |
| `LIMES_DB_USERNAME` | `postgres` | Username of the user that Limes should use to connect to the database. |
| `LIMES_DB_PASSWORD` | *(optional)* | Password for the specified user. |
| `LIMES_DB_HOSTNAME` | `localhost` | Hostname of the database server. |
| `LIMES_DB_PORT` | `5432` | Port on which the PostgreSQL service is running on. |
| `LIMES_DB_CONNECTION_OPTIONS` | *(optional)* | Database connection options. |
| `OS_...` | *(required)* | A full set of OpenStack auth environment variables for Limes's service user. See [documentation for openstackclient](https://docs.openstack.org/python-openstackclient/latest/cli/man/openstack.html) for details. |

## Environment variables for `limes serve` only

| Variable | Default | Description |
| --- | --- | --- |
| `LIMES_API_LISTEN_ADDRESS` | `:80` | Bind address for the HTTP API exposed by this service, e.g. `127.0.0.1:80` to bind only on one IP, or `:80` to bind on all interfaces and addresses. |
| `LIMES_API_POLICY_PATH` | `/etc/limes/policy.yaml` | Path to the oslo.policy file that describes authorization behavior for this service. Please refer to the [OpenStack documentation on policies][policy] for syntax reference. This repository includes an [example policy][ex-pol] that can be used for development setups, or as a basis for writing your own policy. For `:raise`, `:raise_lowpriv`, `:lower` and `:set_rate_limit` policies, the object attribute `%(service_type)s` is available to restrict editing to certain service types. |

### Audit trail

Limes logs all quota changes at the domain and project level in an Open Standards [CADF format](https://www.dmtf.org/standards/cadf). These audit events can be sent to a RabbitMQ server which can then forward them to any cloud audit API, datastore, etc.

| Variable | Default | Description |
| --- | --- | --- |
| `LIMES_AUDIT_ENABLE` | `false` | Set this to true if you want to send the audit events to a RabbitMQ server. |
| `LIMES_AUDIT_QUEUE_NAME` | *(required if auditing is enabled)* | Name for the queue that will hold the audit events. The events are published to the default exchange. |
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
bursting:
  max_multiplier: 0.2
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
| `lowpriv_raise` | no | Configuration options for low-privilege quota raising. See [*low-privilege quota raising*](#low-privilege-quota-raising) for details. |
| `resource_behavior` | no | Configuration options for special resource behaviors. See [*resource behavior*](#resource-behavior) for details. |
| `bursting.max_multiplier` | no | If given, permits quota bursting in this cluster. When projects enable quota bursting, the backend quota is set to `quota * (1 + max_multiplier)`. In the future, Limes may autonomously adjust the multiplier between 0 and the configured maximum based on cluster-wide resource utilization. |
| `quota_distribution_configs` | no | Configuration options for selecting resource-specific quota distribution models. See [*quota distribution models*](#quota-distribution-models) for details. |

### Low-privilege quota raising

The Oslo policy for Limes (see [example policy][ex-pol]) is structured such that raising quotas requires
a different (usually higher) permission level than lowering quotas. However, through the `*:raise_lowpriv` rules,
low-privilege users can be permitted to raise quotas within certain boundaries.

| Field | Required | Description |
| --- | --- | --- |
| `lowpriv_raise.limits.projects` | no | Limits up to which project quotas can be raised by a low-privilege user. |
| `lowpriv_raise.limits.domains` | no | Limits up to which domain quotas can be raised by a low-privilege user. |
| `lowpriv_raise.except_projects_in_domains` | no | May contain a regex. If given, low-privilege quota raising will not be allowed for projects in domains whose names match the regex. |
| `lowpriv_raise.only_projects_in_domains` | no | May contain a regex. If given, low-privilege quota raising will only be possible for projects in domains whose names match the regex. If `except_projects_in_domains` is also given, it takes precedence over `only_projects_in_domains`. |

Both `limits.projects` and `limits.domains` contain two-level maps, first by service type, then by resource name.

- Values may also be specified relative to the cluster capacity with the special syntax `<number>% of cluster capacity`.
- For `limits.domains` only, the alternative special syntax `until <number>% of cluster capacity is assigned` will cause
  quota requests to be approved as long as the sum of all domain quotas is below the given value.

For example:

```yaml
lowpriv_raise:
  limits:
    projects:
      compute:
        cores: 10
        instances: 0.5% of cluster capacity
        ram: 10 GiB
    domains:
      compute:
        cores: 1000
        instances: until 80% of cluster capacity is assigned
        ram: 1 TiB
```

### Resource behavior

Some special behaviors for resources can be configured in the `resource_behavior[]` section. Each entry in this section can match multiple resources.

| Field | Required | Description |
| --- | --- | --- |
| `resource_behavior[].resource` | yes | Must contain a regex. The behavior entry applies to all resources where this regex matches against a slash-concatenated pair of service type and resource name. The anchors `^` and `$` are implied at both ends, so the regex must match the entire phrase. |
| `resource_behavior[].scope` | yes | May contain a regex. The behavior entry applies to matching resources in all domains where this regex matches against the domain name, and in all projects where this regex matches against a slash-concatenated pair of domain and project name, i.e. `domainname/projectname`. The anchors `^` and `$` are implied at both ends, so the regex must match the entire phrase. This regex is ignored for cluster-level resources. |
| `resource_behavior[].max_burst_multiplier` | no | If given, the bursting multiplier for matching resources will be restricted to this value (see also `bursting.max_multiplier`). |
| `resource_behavior[].overcommit_factor` | no | If given, capacity for matching resources will be computed as `raw_capacity * overcommit_factor`, where `raw_capacity` is what the capacity plugin reports. |
| `resource_behavior[].scales_with` | no | If a resource is given, matching resources scales with this resource. The other resource may be specified by its name (for resources within the same service type), or by a slash-concatenated pair of service type and resource name, e.g. `compute/cores`. |
| `resource_behavior[].scaling_factor` | yes, if `scales_with` is given | The scaling factor that will be reported for these resources' scaling relation. |
| `resource_behavior[].min_nonzero_project_quota` | no | A lower boundary for project quota values that are not zero. |
| `resource_behavior[].commitment_durations` | no | If given, commitments for this resource can be created with any of the given durations. The duration format is the same as in the `commitments[].duration` attribute that appears on the resource API. If empty, this resource does not accept commitments. |
| `resource_behavior[].commitment_is_az_aware` | no | If true, commitments for this resource must be created in a specific AZ (i.e. not in a pseudo-AZ). If false, commitments for this resource must be created in the pseudo-AZ `any`. Ignored if `commitment_durations` is empty. |
| `resource_behavior[].commitment_min_confirm_date` | no | If given, commitments for this resource will always be created with `confirm_by` no earlier than this timestamp. This can be used to plan the introduction of commitments on a specific date. Ignored if `commitment_durations` is empty. |
| `resource_behavior[].annotations` | no | A map of extra key-value pairs that will be inserted into matching resources as-is in responses to GET requests, e.g. at `project.services[].resources[].annotations`. |

For example:

```yaml
resource_behavior:
  - { resource: network/healthmonitors, scales_with: network/loadbalancers, scaling_factor: 1 }
  - { resource: network/listeners,      scales_with: network/loadbalancers, scaling_factor: 2 }
  - { resource: network/l7policies,     scales_with: network/loadbalancers, scaling_factor: 1 }
  - { resource: network/pools,          scales_with: network/loadbalancers, scaling_factor: 1 }
  # matches both sharev2/share_capacity and sharev2/snapshot_capacity
  - { resource: sharev2/.*_capacity, overcommit_factor: 2 }
  # disable bursting for the domain "foo"
  - { resource: .*, scope: foo/.*, max_burst_multiplier: 0 }
  # require each project to take at least 100 GB of object storage if they use it at all
  - { resource: object-store/capacity, min_nonzero_project_quota: 107374182400 }
  # starting in 2024, offer commitments for Cinder storage
  - { resource: volumev2/capacity, commitment_durations: [ 1 year, 2 years, 3 years ], commitment_is_az_aware: true, commitment_min_confirm_date: 2024-01-01T00:00:00Z }
```

### Quota distribution models

Each resource uses one of several quota distribution models, with `hierarchical` being the default.

Resource-specific distribution models can be configured per resource in the `quota_distribution_configs[]` section. Each
entry in this section can match multiple resources. Because the semantics of distribution models cross the boundaries of
individual scopes, a distribution model config cannot be limited to certain scopes (like a resource behavior can). It
always applies to the entire resource across all scopes.

| Field | Required | Description |
| --- | --- | --- |
| `quota_distribution_configs[].resource` | yes | Must contain a regex. The config entry applies to all resources where this regex matches against a slash-concatenated pair of service type and resource name. The anchors `^` and `$` are implied at both ends, so the regex must match the entire phrase. |
| `quota_distribution_configs[].model` | yes | The string "hierarchical". |

A distribution model config can contain additional options based on which model is chosen (see below).

#### Model: `hierarchical` (default)

In this model, quota is distributed to domains by the cloud admins
(according to the `domain:{raise,raise_lowpriv,lower}` policies), and then the projects by the domain admins
(according to the `project:{raise,raise_lowpriv,lower}` policies). Domains and projects start out at zero quota.
This model does not allow AZ-aware quotas.

| Field | Required | Description |
| --- | --- | --- |
| `quota_distribution_configs[].strict_domain_quota_limit` | no | Reject attempts to increase domain quotas when the sum of all domain quotas would exceed the cluster capacity. |

#### Model: `autogrow`

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

| Field | Default | Description |
| --- | --- | --- |
| `quota_distribution_configs[].autogrow.allow_quota_overcommit` | `false` | Always give out the desired quota and base quota to all projects, even if the sum of all project quotas ends up being greated than the resource's capacity. |
| `quota_distribution_configs[].autogrow.project_base_quota` | `0` | The minimum amount of quota that will always be given to a project even if it does not have that much commitments or usage to warrant it under the regular formula. Can be set to zero to force projects to bootstrap their quota via commitments. |
| `quota_distribution_configs[].autogrow.growth_multiplier` | *(required)* | As explained above. Cannot be set to less than 1 (100%). Can be set to exactly 1 to ensure that no additional quota will be granted above existing usage and/or confirmed commitments. |
| `quota_distribution_configs[].autogrow.growth_minimum` | `1` | When multiplying a growth baseline greater than 0 with a growth multiplier greater than 1, ensure that the result is at least this much higher than the baseline. |
| `quota_distribution_configs[].usage_data_retention_period` | *(required)* | As explained above. Must be formatted as a string that [`time.ParseDuration`](https://pkg.go.dev/time#ParseDuration) understands. Cannot be set to zero. To only use current usage when calculating quota, set this to a very short interval like `1m`. |

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
```

The area for this service is `compute`.

| Resource | Unit |
| --- | --- |
| `cores` | countable, AZ-aware |
| `instances` | countable, AZ-aware |
| `ram` | MiB, AZ-aware |
| `server_groups` | countable |
| `server_group_members` | countable |

#### Instance price classes

```yaml
services:
  - type: compute
    params:
      bigvm_min_memory: 1048576
```

Instances can be split into two price groups (regular and big VMs) by setting the `params.bigvm_min_memory` key as
shown above. When enabled, the following extra resources become available:

| Resource | Unit |
| --- | --- |
| `cores_regular` | countable, AZ-aware |
| `cores_bigvm` | countable, AZ-aware |
| `instances_regular` | countable, AZ-aware |
| `instances_bigvm` | countable, AZ-aware |
| `ram_regular` | MiB, AZ-aware |
| `ram_bigvm` | MiB, AZ-aware |

Those resources don't track quota; only usage will be reported. Each of these resources has a `contained_in` relation,
e.g. `instances_regular` is `contained_in: "instances"`.

Note that, as of now, this configuration is laser-focused on one specific usecase in SAP Converged Cloud. It might
become more generic once I have more than this singular usecase and a general pattern arises.

#### Instance subresources

```yaml
services:
  - type: compute
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
| `class` | string | either "regular" or "bigvm" (only if instance price classes are enabled, see above) |
| `os_type` | string | identifier for OS type as configured in image (see below) |
| `hypervisor` | string | identifier for the hypervisor type of this instance (only if configured, see below) |
| `metadata` | object of strings | user-supplied key-value data for this instance as reported by OpenStack Nova |
| `tags` | array of strings | user-supplied tags for this instance as reported by OpenStack Nova |

The `os_type` field contains:
- for VMware images: the value of the `vmware_ostype` property of the instance's image, or
- otherwise: the part after the colon of a tag starting with `ostype:`, e.g. `rhel` if there is a tag `ostype:rhel` on the image.

The value of the `hypervisor` field is determined by looking at the extra specs of the instance's flavor, using matching
rules supplied in the configuration like this:

```yaml
services:
  - type: compute
    params:
      hypervisor_type_rules:
        - match: extra-spec:vmware:hv_enabled # match on extra spec with name "vmware:hv_enabled"
          pattern: '^True$'                   # regular expression
          type: vmware
        - match: flavor-name
          pattern: '^kvm'
          type: kvm
        - match: extra-spec:capabilities:cpu_arch
          pattern: '.+'
          type: none # i.e. bare-metal
```

Rules are evaluated in the order given, and the first matching rule will be taken. If no rule matches, the hypervisor
will be reported as `unknown`. If rules cannot be evaluated because the instance's flavor has been deleted, the
hypervisor will be reported as `flavor-deleted`.

#### Separate instance quotas

On SAP Converged Cloud (or any other OpenStack cluster where Nova carries the relevant patches), there will be an
additional resource `instances_<flavorname>` for each flavor with the `quota:separate = true` extra spec. These resources
behave like the `instances` resource. When subresources are scraped for the `instances` resource, they will also be
scraped for these flavor-specific instance resources. The flavor-specific instance resources are in the `per_flavor`
category.

```yaml
services:
  - type: compute
    params:
      separate_instance_quotas:
        flavor_name_pattern: ^bm_
        flavor_aliases:
          bm_newflavor1: [ bm_oldflavor1 ]
          bm_newflavor2: [ bm_oldflavor2, bm_oldflavor3 ]
```

Sometimes Tempest creates resource classes or flavors that Limes recognizes as requiring a separate instance quota,
which may not be desired. To control which flavors get a separate instance quota, give the
`params.separate_instance_quotas.flavor_name_pattern` option as shown above. Only flavors with a name matching that
regex will be considered.

On some Nova installations, some flavors can have multiple names, either as permanent aliases or temporarily while
moving to a new flavor naming scheme. The `params.separate_instance_quotas.flavor_aliases` option configures Limes to
recognize flavor names that are aliased to each other, and decides which flavor name Limes prefers. For instance, in the
config example above, the names `bm_newflavor2`, `bm_oldflavor2` and `bm_oldflavor3` are all aliases referring to the
same flavor, and Limes prefers the name `bm_newflavor2`. The preferred name will be used when deriving a resource name
for the respective separate instance quota. In the previous example, the resource will be called
`instances_bm_newflavor2` since `bm_newflavor2` is the flavor alias that Limes prefers.

### `dns`: Designate v2

```yaml
services:
  - type: dns
```

The area for this service is `dns`.

| Resource | Unit |
| --- | --- |
| `recordsets` | countable |
| `zones` | countable |

When the `recordsets` quota is set, the backend quota for records is set to 20 times that value, to fit into the `records_per_recordset` quota (which is set to 20 by default in Designate). The record quota cannot be controlled explicitly in Limes.

### `email-aws`: Cronus v1 (SAP Converged Cloud only)

```yaml
services:
  - type: email-aws
```

The area for this service is `email`. This service has no resources, only rates.

| Rate | Unit | Comment |
| --- | --- | --- |
| `attachments_size` | bytes | Size of attachments for outgoing emails. |
| `data_transfer_in` | bytes | Total size of incoming emails. |
| `data_transfer_out` | bytes | Total size of outgoing emails. |
| `recipients` | countable | Number of recipients on outgoing emails. |

### `endpoint-services`: [Archer](https://github.com/sapcc/archer) v1

```yaml
services:
  - type: endpoint-services
```

The area for this service is `network`.

| Resource | Unit |
| --- | --- |
| `endpoints` | countable |
| `services` | countable |

### `keppel`: Keppel v1

```
services:
  - type: keppel
```

The area for this service is `storage`.

| Resource | Unit |
| --- | --- |
| `images` | countable |

### `network`: Neutron v1

```yaml
services:
  - type: network
```

The area for this service is `network`. Resources are categorized into `networking` for SDN resources and `loadbalancing` for LBaaS resources.

| Category | Resource | Unit | Comment |
| --- | --- | --- | --- |
| `networking` | `floating_ips` | countable ||
|| `networks` | countable ||
|| `ports` | countable ||
|| `rbac_policies` | countable ||
|| `routers` | countable ||
|| `security_group_rules` | countable | See note about auto-approval below. |
|| `security_groups` | countable | See note about auto-approval below. |
|| `subnet_pools` | countable ||
|| `subnets` | countable ||
|| `bgpvpns` | countable | Only when Neutron has the `bgpvpn` extension. |
|| `trunks` | countable | Only when Neutron has the `trunk` extension. |
| `loadbalancing` | `healthmonitors` | countable ||
|| `l7policies` | countable ||
|| `listeners` | countable ||
|| `loadbalancers` | countable ||
|| `pools` | countable ||
|| `pool_members` | countable ||

When a new project is scraped for the first time, and usage for `security_groups` and `security_group_rules` is 1 and 4,
respectively, quota of the same size is approved automatically. This covers the `default` security group that is
automatically created in a new project.

### `object-store`: Swift v1

```yaml
services:
  - type: object-store
```

The area for this service is `storage`.

| Resource | Unit |
| --- | --- |
| `capacity` | Bytes |

### `sharev2`: Manila v2

```yaml
services:
  - type: sharev2
    params:
      share_types:
        - name: default
          replication_enabled: true
        - name: hypervisor_storage
          mapping_rules:
            - { name_pattern: "fooproject-.*", share_type: hypervisor_storage_foo }
            - { name_pattern: ".*@bardomain",  share_type: hypervisor_storage_bar }
            - { name_pattern: ".*",            share_type: '' }
      prometheus_api_for_az_awareness:
        url: https://prometheus.example.com
        cert:    /path/to/client.pem
        key:     /path/to/client-key.pem
        ca_cert: /path/to/server-ca.pem
      prometheus_api_for_netapp_metrics:
        url: https://prometheus.example.com
        cert:    /path/to/client.pem
        key:     /path/to/client-key.pem
        ca_cert: /path/to/server-ca.pem
```

The area for this service is `storage`. The following resources are always exposed:

| Resource | Unit | Comment |
| --- | --- | --- |
| `shares` | countable, AZ-aware | |
| `share_capacity` | GiB, AZ-aware | |
| `share_snapshots` | countable, AZ-aware | |
| `snapshot_capacity` | GiB, AZ-aware | |
| `share_networks` | countable | |
| `snapmirror_capacity` | GiB | Only if `prometheus_api_for_netapp_metrics` is given. A SAP-specific extension that reports disk space consumed by Snapmirror backups. |

AZ awareness for share and snapshot data requires that `prometheus_api_for_az_awareness` contains the following metrics, each with label dimensions `project_id`, `vailability_zone_name` and `share_type_id`:

- `openstack_manila_replicas_count_gauge` (number of shares incl. replicas)
- `openstack_manila_replicas_size_gauge` (size in GiB of shares incl. replicas)
- `openstack_manila_snapshot_count_gauge` (number of snapshots)
- `openstack_manila_snapshot_size_gauge` (size in GiB of snapshots)

#### Multiple share types

If the `params.share_types` field lists more than one share type, the first
four of the aforementioned five resources will refer to the quota for the first
of these share types. (This peculiar rule exists for backwards-compatibility
reasons.) For each other share type, the following resources are exposed:

| Resource | Unit | Comment |
| --- | --- | --- |
| `shares_${share_type}` | countable | |
| `share_capacity_${share_type}` | GiB | |
| `share_snapshots_${share_type}` | countable | |
| `snapshot_capacity_${share_type}` | GiB | |

The quota values in Manila are assigned as follows:

- When share replica quotas are available (in Victoria and above), share types
  that have `replication_enabled: true` in our configuration will have their
  replica and replica capacity quotas set to the same value as the share and
  share capacity quotas. (This makes sense since the shares themselves also use
  up replica quota.) If `replication_enabled` is false or unset, the replica
  quotas will be set to 0 instead.
- Besides the share-type-specific quotas, the general quotas are set to the sum
  across all share types.

#### Virtual share types

Multiple Manila share types can be grouped into a single set of resources by adding `mapping_rules` to the share type as
shown in the example above for the `hypervisor_storage` share type. In this case, `hypervisor_storage` is the share type
name from which the Limes resource names are derived, but the actual Manila share types (for which quota is set and for
which usage is retrieved) are `hypervisor_storage_foo` and `hypervisor_storage_bar`.

In each mapping rule, `name_pattern` is a regex that is matched against `$PROJECT_NAME@$DOMAIN_NAME` (with `^` and `$`
automatically implied at the end of the regex). Mapping rules are evaluated in order: The first matching pattern
determines the Manila share type for that particular project scope. If no mapping rule matches, the original share type is used unaltered.

If the matching mapping rule sets the share type to the empty string, this share type is ignored for this project in the
following way: For each resource belonging to this share type,

- usage is always reported as 0, and
- trying to set a non-zero quota is an error.

#### Physical usage

Optionally, when the `params.prometheus_api` configuration option is set,
physical usage data will be scraped using the Prometheus metrics exported by
the [netapp-api-exporter](https://github.com/sapcc/netapp-api-exporter).

Only the `prometheus_api.url` field is required. You can pin the server's CA
certificate (`prometheus_api.ca_cert`) and/or specify a TLS client certificate
(`prometheus_api.cert`) and private key (`prometheus_api.key`) combination that
will be used by the HTTP client to make requests to the Prometheus API.

### `volumev2`: Cinder v3

The service type name refers to the v2 API for backwards compatibility reasons.

```yaml
services:
  - type: volumev2
    params:
      volume_types: [ vmware, vmware_hdd ]
      with_volume_subresources: true
      with_snapshot_subresources: true
```

The area for this service is `storage`. The following resources are always exposed:

| Resource | Unit |
| --- | --- |
| `capacity` | GiB, AZ-aware |
| `snapshots` | countable, AZ-aware |
| `volumes` | countable, AZ-aware |

If the `params.volume_types` field lists more than one volume type, the
aforementioned resources will refer to the quota for the first of these volume
types. (This peculiar rule exists for backwards-compatibility reasons.) For
each other volume type, the following resources are exposed:

| Resource | Unit |
| --- | --- |
| `capacity_${volume_type}` | GiB, AZ-aware |
| `snapshots_${volume_type}` | countable, AZ-aware |
| `volumes_${volume_type}` | countable, AZ-aware |

In Cinder, besides the volume-type-specific quotas, the general quotas
(`gigabytes`, `snapshots`, `volumes`) are set to the sum across all volume
types.

When subresource scraping is enabled (as shown above) for the `volumes` and `volumes_${volume_type}` resources,
volume subresources bear the following attributes:

| Attribute | Type | Comment |
| --- | --- | --- |
| `id` | string | volume UUID |
| `name` | string | volume name |
| `status` | string | volume status [as reported by OpenStack Cinder](https://developer.openstack.org/api-ref/block-storage/v2/index.html#volumes-volumes) |
| `size` | integer value with unit | volume size |
| `availability_zone` | string | availability zone where volume is located |

When subresource scraping is enabled (as shown above) for the `snapshots` and `snapshots_${volume_type}` resources,
snapshot subresources bear the following attributes:

| Attribute | Type | Comment |
| --- | --- | --- |
| `id` | string | snapshot UUID |
| `name` | string | snapshot name |
| `status` | string | snapshot status [as reported by OpenStack Cinder](https://developer.openstack.org/api-ref/block-storage/v2/index.html#volumes-volumes) |
| `size` | integer value with unit | snapshot size |
| `volume_id` | string | UUID of volume from which snapshot was created (if any) |

## Available capacity plugins

Note that capacity for a resource only becomes visible when the corresponding service is enabled in the
`services` list as well.

Capacity plugins have a `type` (as specified in the subheadings for this section) and an `id`. For most capacity
plugins, having more than one does not make sense because they refer to other service's APIs (which only exist once per
cluster anyway). In these cases, `id` is conventionally set to the same as `type`. For capacity plugins like
`prometheus` or `manual`, it's possible to have multiple plugins of the same type at once. In that case, the `id` must
be unique for each plugin.

### `cinder`

```yaml
capacitors:
  - id: cinder
    type: cinder
    params:
      volume_types:
        vmware:     { volume_backend_name: vmware_ssd, default: true }
        vmware_hdd: { volume_backend_name: vmware_hdd, default: false }
      with_subcapacities: true
```

| Resource | Method |
| --- | --- |
| `volumev2/capacity` | The sum over all pools reported by Cinder with `volume_backend_name` matching that of the default volume type. |
| `volumev2/capacity_${volume_type}` | The sum over all pools reported by Cinder with `volume_backend_name` matching that of the given non-default volume type. |

No estimates are made for the `snapshots` and `volumes` resources since capacity highly depends on
the concrete Cinder backend.

When subcapacity scraping is enabled (as shown above), subcapacities will be scraped for the respective resources. Each
subcapacity corresponds to one Cinder pool, and bears the following attributes:

| Name | Type | Comment |
| --- | --- | --- |
| `pool_name` | string | The pool name as reported by Cinder. |
| `az` | string | The pool's availability zone. The AZ is determined by matching the pool's hostname against the list of services configured in Cinder. |
| `capacity_gib` | integer | Total capacity of this pool in GiB. This corresponds to the pool's `total_capacity_gb` attribute in Cinder. |
| `usage_gib` | integer | Usage level of this pool in GiB. This corresponds to the pool's `allocated_capacity_gb` attribute in Cinder. |

### `manila`

```yaml
capacitors:
- id: manila
  type: manila
  params:
    share_types:
      - name: default
      - name: hypervisor_storage
        mapping_rules:
          - { name_pattern: "fooproject-.*", share_type: hypervisor_storage_foo }
          - { name_pattern: ".*@bardomain",  share_type: hypervisor_storage_bar }
          - { name_pattern: ".*",            share_type: '' }
    share_networks: 250
    shares_per_pool: 1000
    snapshots_per_share: 5
    capacity_balance: 0.5
    with_subcapacities: true
```

| Resource | Method |
| --- | --- |
| `sharev2/share_networks` | Taken from identically-named configuration parameter. |
| `sharev2/shares` | Calculated as `shares_per_pool * count(pools) - share_networks`. |
| `sharev2/share_snapshots` | Calculated as `snapshots_per_share` times the above value. |
| `sharev2/share_capacity`<br>`sharev2/snapshot_capacity` | Calculated as `sum(pool.capabilities.totalCapacityGB)`, then divided among those two resources according to the `capacity_balance` (see below). |

The last four of these five resources consider only pools with the share type
that appears first in `params.share_types` (to match the behavior of the quota
plugin). For any other share type listed in `params.share_types`, capacities
will be reported analogously for `sharev2/shares_${share_type}` etc. by
considering pools with that share type.

The `mapping_rules` inside a share type have the same semantics as for the `sharev2` quota plugin, and must be set
identically to ensure that the capacity values make sense in context.

When subcapacity scraping is enabled (as shown above), subcapacities will be scraped for the `share_capacity` and
`snapshot_capacity` resources. Each subcapacity corresponds to one Manila pool, and bears the following attributes:

| Name | Type | Comment |
| --- | --- | --- |
| `pool_name` | string | The pool name as reported by Manila. |
| `az` | string | The pool's availability zone. The AZ is determined by matching the pool's hostname against the list of services configured in Manila. |
| `capacity_gib` | integer | Total capacity for shares/snapshots in GiB. This is based on the pool's `total_capacity_gb` attribute in Manila. |
| `usage_gib` | integer | Usage level for shares/snapshots in GiB. This is based on the pool's `allocated_capacity_gb` attribute in Manila. |
| `exclusion_reason` | string | If filled (see below), the pool's capacity and usage is not counted towards the global and AZ-wide totals. |

As a SAP-specific extension, the pool capability field `hardware_state` is recognized to ignore capacity that is not
marked as live. Ignored pools will still show up in the subcapacities, but their `exclusion_reason` field will be filled.

#### Capacity balance

The capacity balance is defined as

```
snapshot_capacity = capacity_balance * share_capacity,
```

that is, there is `capacity_balance` as much snapshot capacity as there is share capacity. For example, `capacity_balance = 0.5` means that the capacity for snapshots is half as big as that for shares, meaning that shares get 2/3 of the total capacity and snapshots get the other 1/3.

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

```yaml
capacitors:
  - id: nova
    type: nova
    params:
      hypervisor_selection:
        aggregate_name_pattern: '^(?:vc-|qemu-)'
        hypervisor_type_pattern: '^(?:VMware|QEMU)'
      max_instances_per_aggregate: 10000
      flavor_selection:
        required_extra_specs:
          first: 'foo'
        excluded_extra_specs:
          second: 'bar'
      with_subcapacities: true
```

| Resource | Method |
| --- | --- |
| `compute/cores` | The sum of the reported CPUs for all hypervisors in matching aggregates. Note that the hypervisor statistics reported by Nova do not take overcommit into account, so you may have to configure the overcommitment again in Limes for accurate capacity reporting. |
| `compute/instances` | Estimated as `maxInstancesPerAggregate * count(matchingAggregates)`, but never more than `sumLocalDisk / maxDisk`, where `sumLocalDisk` is the sum of the local disk size for all hypervisors, and `maxDisk` is the largest disk requirement of all flavors. |
| `compute/ram` | The sum of the reported RAM for all hypervisors. |

Only those hypervisors are considered that belong to an aggregate whose name matches the regex in the
`params.hypervisor_selection.aggregate_name_pattern` parameter.
There must be a 1:1 relation between matching aggregates and hypervisors: If a
hypervisor belongs to more than one matching aggregate, an error is raised. The aggregate level is used mostly to
compute the hard limit of the instance capacity (`maxInstancesPerAggregate * count(matchingAggregates)`); if you do not
have a level between AZs that imposes such a hard limit, you can use AZ-wide aggregates as a fallback here.

If the `params.hypervisor_selection.hypervisor_type_pattern` parameter is set, only those hypervisors are considered whose `hypervisor_type`
matches this regex. Note that this is distinct from the `hypervisor_type_rules` used by the `compute` quota plugin, and
uses the `hypervisor_type` reported by Nova instead.

The `params.hypervisor_selection` can also contains lists of strings in the `required_traits` and `excluded_traits` keys.
If given, only those hypervisors will be considered whose resource providers have all of the `required_traits` and none
of the `excluded_traits`.

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
          cores:     sum(hypervisor_cores)
          ram:       sum(hypervisor_ram_gigabytes) * 1024
```

Like the `manual` capacity plugin, this plugin can provide capacity values for arbitrary resources. A [Prometheus][prom]
instance must be running at the URL given in `params.api.url`. Each of the queries in `params.queries` is
executed on this Prometheus instance, and the resulting value is reported as capacity for the resource named by the key
of this query. Queries are grouped by service, then by resource.

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

### `sapcc-ironic`

```yaml
capacitors:
  - id: sapcc-ironic
    type: sapcc-ironic
    params:
      flavor_name_pattern: ^bm_
      flavor_aliases:
        newflavor1: [ oldflavor1 ]
        newflavor2: [ oldflavor2, oldflavor3 ]
      with_subcapacities: true
```

This capacity plugin reports capacity for the special `compute/instances_<flavorname>` resources that exist on SAP
Converged Cloud ([see above](#compute-nova-v2)). For each such flavor, it uses the Ironic node's resource class to
match it to a flavor with the **same name**.

The `params.flavor_name_pattern` and `params.flavor_aliases` parameters have the same semantics as the respective
parameters on the `compute` service type, and should have the same contents as well (unless you like unnecessary
confusion).

```yaml
capacitors:
  - id: sapcc-ironic
    type: sapcc-ironic
subcapacities:
  - compute: [ instances-baremetal ]
```

When subcapacity scraping is enabled (as shown above), subcapacities will be scraped for all resources reported by
this plugin. Subcapacities correspond to Ironic nodes and bear the following attributes:

| Attribute | Type | Comment |
| --- | --- | --- |
| `id` | string | node UUID |
| `name` | string | node name |
| `instance_id` | string | UUID of the Nova instance running on this node (if any) |
| `ram` | integer value with unit | amount of memory |
| `cores` | integer | number of CPU cores |
| `disk` | integer value with unit | root disk size |
| `serial` | string | hardware serial number for node |

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
