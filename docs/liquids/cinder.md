<!--
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

# Liquid: `cinder`

This liquid provides support for the block storage service Cinder.

- The suggested service type is `liquid-cinder`.
- The suggested area is `storage`.

## Service-specific configuration

| Field                                     | Type                    | Description                                                                     |
| ----------------------------------------- | ----------------------- | ------------------------------------------------------------------------------- |
| `with_subcapacities`                      | boolean                 | If true, subcapacities are reported.                                            |
| `with_snapshot_subresources`              | boolean                 | If true, subresources are reported on snapshots resources.                      |
| `with_volume_subresources`                | boolean                 | If true, subresources are reported on volumes resources.                        |
| `manage_private_volume_types`<sup>1</sup> | regexpext.BoundedRegexp | If set, matching private volume types will be considered for liquid reports.    |
| `ignore_public_volume_types`<sup>1</sup>  | regexpext.BoundedRegexp | If set, matching public volume types will not be considered for liquid reports. |

<sup>1</sup> Values are regular expressions [in the Go syntax](https://pkg.go.dev/regexp/syntax). Leading `^` and trailing `$` anchors are implied automatically.

## Resources

For each volume type that can be listed within the project scope of the supplied service user, three resources are reported:

| Resource          | Unit | Capabilities                         |
| ----------------- | ---- | ------------------------------------ |
| `capacity_$TYPE`  | GiB  | HasCapacity = true, HasQuota = true  |
| `snapshots_$TYPE` | None | HasCapacity = false, HasQuota = true |
| `volumes_$TYPE`   | None | HasCapacity = false, HasQuota = true |

When reading quota, the overall quotas are ignored and only the volume-type-specific quotas are reported.
When writing quota, the overall quotas are set to the sum of the respective volume-type-specific quotas.

If `with_volume_subresources` and/or `with_snapshot_subresources` is set, the respective resources will have one subresource for each volume/snapshot, with the following fields:

| Field                 | Type   | Description                                               |
| --------------------- | ------ | --------------------------------------------------------- |
| `id`                  | string | The UUID of the volume or snapshot.                       |
| `name`                | string | The human-readable name of the volume or snapshot.        |
| `attributes.size_gib` | uint64 | The logical size of the volume or snapshot.               |
| `attributes.status`   | string | The status of the volume or snapshot according to Cinder. |

## Capacity calculation

Capacity is calculated as the sum over all storage pools and will be grouped into volume types.
The following methods to assign pools to volume types are implemented:

- Regular Pools are grouped into volume types by matching their `volume_backend_name` against `extra_specs.volume_backend_name` of the volume type.
- Remaining Pools are grouped into volume types by matching their `storage_protocol` and `quality_type` against the `extra_specs.storage_protocol` and
  `extra_secs.quality_type` of the volume type.
- Private are grouped into volume types by matching their `storage_protocol` and `vendor_name` against the `extra_specs.storage_protocol` and
  `extra_secs.vendor_name` of the volume type.

| Matching | Volume Type                      | Pools                            |
| -------- | -------------------------------- | -------------------------------- |
| Type 1   | volume_backend_name              | volume_backend_name              |
| Type 2   | storage_protocol<br>quality_type | storage_protocol<br>quality_type |
| Type 3   | storage_protocol<br>vendor_name  | storage_protocol<br>vendor_name  |

Pools without a matching volume type are ignored.

- Pools are grouped into availability zones by matching the pool's hostname against the list of services configured in Cinder.
  Pools without a matching AZ are reported in AZ `unknown`.

| Pools | Service |
| ----- | ------- |
| name  | host    |

If `with_subcapacities` is set, the capacity resource will have one subcapacity for each pool, with the following fields:

| Field                         | Type              | Description                                                         |
| ----------------------------- | ----------------- | ------------------------------------------------------------------- |
| `name`                        | string            | The name of the pool.                                               |
| `capacity`                    | uint64            | The logical size of the pool, in GiB.                               |
| `usage`                       | uint64            | The logical size of all volumes and snapshots on this pool, in GiB. |
| `attributes.exclusion_reason` | string or omitted | See below for details.                                              |
| `attributes.real_capacity`    | uint64 or omitted | Only shown if different from `capacity`. See below for details.     |

### SAP Converged Cloud extension: Pool exclusion

If a pool has the field `capabilities.custom_attributes.cinder_state` with the value `drain` or `reserved`:

- This pool will only contribute capacity towards the total to the extent to which it is actually used (i.e., not higher than its `usage` value).
- Consequently, the `capacity` field of the subcapacity will also show the `usage` value, with the real capacity value relegated to `attributes.real_capacity`.

The `attributes.exclusion_reason` will explain this by showing the value `cinder_state = drain` or `cinder_state = reserved`, respectively.

We use this to avoid double-counting of capacity when a new filer is brought in to replace an old one.
While the new filer is brought in, it is in state `reserved` to avoid counting its capacity at all.
Once payload is being moved from the old filer, it will be set to state `drain` to avoid the appearance of capacity opening up there as it is being emptied.
Once the old filer is gone, the `reserved` state is removed from the new filer to have it count towards capacity in the normal manner.
