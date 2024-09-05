# Liquid: `manila`

This liquid provides support for the shared file system storage service Manila.

- The suggested service type is `liquid-manila`.
- The suggested area is `storage`.

## Service-specific configuration

| Field | Type | Description |
| ----- | ---- | ----------- |
| `capacity_calculation` | object | Various options relating to capacity calculation. [See "Capacity calculation" for details.](#capacity-calculation) |
| `capacity_calculation.capacity_balance` | float64 | A ratio describing how unused capacity will be distributed between shares and snapshots. |
| `capacity_calculation.share_networks` | uint64 | The reported capacity value for the `share_networks` resource. |
| `capacity_calculation.shares_per_pool` | uint64 | A multiplicative factor for computing capacity for the `shares_$TYPE` resources. |
| `capacity_calculation.snapshots_per_share` | uint64 | A multiplicative factor for computing capacity for the `snapshots_$TYPE` resources. |
| `capacity_calculation.with_subcapacities` | boolean | If set to true, subcapacities will be reported on all resources that have "capacity" in their name. |
| `prometheus_api_for_az_awareness` | object | If given, specifies a connection to a Prometheus API providing AZ awareness metrics. [See "AZ awareness metrics" for details.](#sap-converged-cloud-extension-az-awareness-metrics) |
| `prometheus_api_for_netapp_metrics` | object | If given, specifies a connection to a Prometheus API providing NetApp metrics. [See "NetApp metrics" for details.](#sap-converged-cloud-extension-netapp-metrics) |
| `share_types` | list of objects | Required. Each object contains configuration for one share type that the liquid manages. |
| `share_types[].name` | string | Required. The name of the share type on the Manila API. |
| `share_types[].replication_enabled` | boolean | Whether this share type supports share replicas. This affects usage measurements and quota application as described below. |
| `share_types[].mapping_rules` | list of objects | If given, this share type is a virtual share type mapping to multiple actual share types. [See "Virtual Share Types" for details.](#virtual-share-types) |

The two `prometheus_api_...` objects may contain the following fields (if they are present at all):

| Field | Type | Description |
| ----- | ---- | ----------- |
| `url` | string | Required. The base URL of the Prometheus API, usually without any subpath (e.g. `https://metrics.example.com`). |
| `cacert` | string | If given, must contain a path to a PEM-encoded certificate bundle. Must be given if the Prometheus API is behind TLS, using a server certificate not signed by a CA in the system-wide trust root bundle. |
| `cert` | string | If given, must contain a path to a PEM-encoded certificate bundle. The first certificate in the bundle will be presented as client certificate when connecting to the Prometheus API. The bundle may contain additional certificates as required to establish a trust chain from the client certificate to a CA trusted by the server. |
| `key` | string | Required if and only if `cert` is given. Must contain a path to a PEM-encoded private key belonging to the client certificate from `cert`. |

## Resources

One resource is always reported:

| Resource         | Unit | Capabilities                        |
| ---------------- | ---- | ----------------------------------- |
| `share_networks` | GiB  | HasCapacity = true, HasQuota = true |

For each configured share type, the following resources are reported:

| Resource                    | Unit | Capabilities                         | Notes                         |
| --------------------------- | ---- | ------------------------------------ | ----------------------------- |
| `shares_$TYPE`              | None | HasCapacity = true, HasQuota = true  |                               |
| `snapshots_$TYPE`           | None | HasCapacity = true, HasQuota = true  |                               |
| `share_capacity_$TYPE`      | GiB  | HasCapacity = true, HasQuota = true  |                               |
| `snapshot_capacity_$TYPE`   | GiB  | HasCapacity = true, HasQuota = true  |                               |
| `snapmirror_capacity_$TYPE` | GiB  | HasCapacity = true, HasQuota = true  | only if configured, see below |

When the share type is configured as `replication_enabled`, quota and usage for replicas is considered as follows:

- On read, the resource `shares_$TYPE` will report quota and usage for the number of replicas instead of for the number of shares.
  If these two quotas are not identical in Manila, a quota value of -1 will be reported instead to force an immediate resync in Limes.
- On write, the quota for the resource `shares_$TYPE` will be written as the quota for both the number of shares and the number of replicas.
- The same rules apply for the `share_capacity_$TYPE` resource, which models quota and usage for both share capacity and replica capacity.

The intent of these rules is to ensure that users do not have to manage the shares and replicas resources separately.
Managing quotas like this works because shares also count towards the replica quota (a share is considered its own first replica).

### Virtual share types

Multiple Manila share types can be grouped into a single **virtual share type** by adding mapping rules to the share type configuration as
in the following example:

```json
{
  "share_types": [
    {
      "name": "default",
      "replication_enabled": true
    },
    {
      "name": "hypervisor_storage",
      "mapping_rules": [
        { "match_project_name": "fooproject-.*", "name": "hypervisor_storage_foo" },
        { "match_project_name": ".*@bardomain",  "name": "hypervisor_storage_bar" },
        { "match_project_name": ".*",            "name": "" }
      ]
    }
  ]
}
```

In this case, `hypervisor_storage` is the share type name from which the Limes resource names are derived,
but the actual Manila share types (for which quota is set and for which usage is retrieved)
are `hypervisor_storage_foo` and `hypervisor_storage_bar`.

In each mapping rule, `match_project_name` is a regex that is matched against `$PROJECT_NAME@$DOMAIN_NAME`
(with `^` and `$` automatically implied at the end of the regex).
Mapping rules are evaluated in order: The first matching pattern determines the Manila share type for that particular project scope.
If no mapping rule matches, the main share type name is used.

If the matching mapping rule sets the share type to the empty string, the `forbidden` flag will be set on all relevant resources
to hide the share type from the respective project in Limes.

Capacity calculation disregards the mapping rules and queries for pools using the virtual share type.
The intent of this facility is to provide specialized share types to different projects that access the same capacity.

### SAP Converged Cloud extension: AZ awareness metrics

By default, all resources are non-AZ-aware, and report their usage into AZ `unknown` (except for share networks which are reported in AZ `any`).
If `prometheus_api_for_az_awareness` is configured, this Prometheus instance will be queried to breakdown the overall usage numbers into AZ-aware usage numbers.
For this, the following metric families must be present in Prometheus, each with label dimensions `project_id`, `availability_zone_name` and `share_type_id`:

- `openstack_manila_replicas_count_gauge` (number of shares incl. replicas)
- `openstack_manila_replicas_size_gauge` (size in GiB of shares incl. replicas)
- `openstack_manila_snapshot_count_gauge` (number of snapshots)
- `openstack_manila_snapshot_size_gauge` (size in GiB of snapshots)

### SAP Converged Cloud extension: NetApp metrics

In SAP Converged Cloud, Manila is backed by NetApp storages.
The custom [netapp-api-exporter](https://github.com/sapcc/netapp-api-exporter) is used to enhance the usage data reported by this liquid:

- Physical usage can be reported for the `share_capacity_${TYPE}` and `snapshot_capacity_${TYPE}` resources.
- Additional resources `snapmirror_capacity_${TYPE}` will be present on each share type to count the usage and physical usage by replicas that are created through the NetApp snapmirror mechanism without being known to Manila.

## Capacity calculation

Capacity within each virtual share type is calculated as the sum over all storage pools:

- Pools are queried for each relevant real share type, and the results are merged into a single list.
  Pools that do not match any of the configured real share types across all virtual share types are ignored.
- Pools are grouped into availability zones by matching the pool's hostname against the list of services configured in Manila.
  Pools without a matching AZ are reported in AZ `unknown`.

The sum of all pool capacities is then divided up between share capacity, snapshot capacity and (if enabled) snapmirror capacity according to the resource demand signaled by Limes.
If any capacity remains unallocated afterwards because there was no demand for it, it is split between share capacity and snapshot capacity according to the `capacity_balance` configuration parameter.
The capacity balance is defined as the ratio between capacity given to snapshots and capacity to shares.
For example, a capacity balance of 2 will result in twice as much capacity given to snapshots than to shares (i.e. two thirds of the unallocated capacity are given to snapshots, and one third to shares).

Within each AZ, the countable resources are assigned capacity as follows:

```
shares := max(0, shares_per_pool * number of pools - share_networks / number of AZs)
snapshots := snapshots_per_share * snapshots
```

If `with_subcapacities` is set, the share capacity resource will have one subcapacity for each pool, with the following fields:

| Field | Type | Description |
| ----- | ---- | ----------- |
| `name` | string | The name of the pool. |
| `capacity` | uint64 | The logical size of the pool, in GiB. |
| `usage` | uint64 | The logical size of all volumes and snapshots on this pool, in GiB. |
| `attributes.exclusion_reason` | string or omitted | See below for details. |
| `attributes.real_capacity` | uint64 or omitted | Only shown if different from `capacity`. See below for details. |

### SAP Converged Cloud extension: Pool exclusion

If a pool has the field `capabilities.hardware_state` with the value `in_build`, `in_decom` or `replacing_decom`:

- This pool will only contribute capacity towards the total to the extent to which it is actually used (i.e., not higher than its `usage` value).
- Consequently, the `capacity` field of the subcapacity will also show the `usage` value, with the real capacity value relegated to `attributes.real_capacity`.

The `attributes.exclusion_reason` will explain this by showing a value like `hardware_state = in_build`.

We use this to avoid double-counting of capacity when a new filer is brought in to replace an old one.
While the new filer is brought in, it is in state `in_build` to avoid counting its capacity at all.
Once payload is being moved from the old filer, the old and new filer will be set to state `in_decom` and `replacing_decom`, respectively, to avoid the appearance of capacity opening up on the old filer as it is being emptied.
Once the old filer is gone, the `replacing_decom` state is updated to `live` on the new filer to have it count towards capacity in the normal manner.
