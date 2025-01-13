# Liquid: `nova`

This liquid provides support for the compute service Nova.

- The suggested service type is `liquid-nova`.
- The suggested area is `compute`.

## Service-specific configuration

| Field | Type | Description |
| ----- | ---- | ----------- |
| `binpack_behavior.score_ignores_cores`<br>`binpack_behavior.score_ignores_disk`<br>`binpack_behavior.score_ignores_ram` | boolean | If true, when ranking nodes during placement, do not include the respective dimension in the score. (This only affects instances of split flavors. [See below](#split-flavors) below for details.) |
| `flavor_selection.excluded_extra_specs` | map[string]string | Exclude flavors that have any of these extra specs. |
| `flavor_selection.required_extra_specs` | map[string]string | Only match flavors that have all of these extra specs. |
| `hypervisor_selection.aggregate_name_pattern` | regexp | Only match hypervisors that reside in an aggregate matching this pattern. If a hypervisor resides in multiple matching aggregates, an error is raised. |
| `hypervisor_selection.hypervisor_type_pattern` | regexp | Only match hypervisors with a `hypervisor_type` attribute matching this pattern. |
| `hypervisor_selection.required_traits` | []string | Only those hypervisors will be considered whose resource providers have all of the traits without `!` prefix and none of those with `!` prefix. |
| `hypervisor_selection.shadowing_traits` | []string | If a hypervisor matches any of the rules in this configuration field (using the same logic as above for `required_traits`), the hypervisor will be considered shadowed. Its capacity will not be counted. (This affects capacity calculation for split flavors. [See below](#split-flavors) for details.) |
| `ignored_traits` | []string | Traits that should be ignored during confirmation that all pooled flavors agree on which trait-match extra specs they use.  |
| `with_subcapacities` | boolean | If true, subcapacities are reported. |
| `with_subresources` | boolean | If true, subresources are reported. |

## Resources

The standard roster of Nova quotas is supported:

| Resource | Unit | Capabilities |
| --- | --- | --- |
| `cores` | None | HasCapacity = true, HasQuota = true |
| `instances` | None | HasCapacity = true, HasQuota = true |
| `ram` | MiB | HasCapacity = true, HasQuota = true |
| `server_groups` | None | HasCapacity = false, HasQuota = true |
| `server_group_members` | None | HasCapacity = false, HasQuota = true |

Additionally, there is one resource for each flavor that carries the `quota:separate = "true"` extra spec:

| Resource | Unit | Capabilities |
| --- | --- | --- |
| `instances_${FLAVOR_NAME}` | None | HasCapacity = true, HasQuota = true |

These flavors are called **split flavors** in this documentation (since their quota is split from the usual quotas).
All other flavors are called **pooled flavors** (since they draw from the default quota pool for `cores`, `instances` and `ram`).
Resources for split flavors will not be spawned for Ironic flavors (those with extra spec `capabilities:hypervisor_type = "ironic"`).

If `with_subresources` is set, each `instances` or `instances_$FLAVOR` resource will have one subresource for each instance of the respective flavor(s), with the following fields:

| Field | Type | Description |
| ----- | ---- | ----------- |
| `id` | string | The UUID of the Nova instance. |
| `name` | string | The human-readable name of the Nova instance. |
| `attributes.status` | string | The status of the instance according to Nova. |
| `attributes.metadata` | object of strings | User-supplied key-value data on this instance according to Nova. |
| `attributes.tags` | array of strings | User-supplied tags on this instance according to Nova. |
| `attributes.flavor.name` | string | The name of this instance's flavor. |
| `attributes.flavor.vcpu` | integer | The number of virtual cores available to this instance. |
| `attributes.flavor.ram_mib` | integer | The amount of RAM available to this instance, in MiB. |
| `attributes.flavor.disk_gib` | integer | The amount of local disk available to this instance, in GiB. |
| `attributes.flavor.video_ram_mib` | integer | The amount of video RAM available to this instance, in MiB. |
| `attributes.os_type` | string | The OS type, as inferred from the image that was used to boot this instance. [See below](#os-type-inference) for details. |

### Considerations for cloud operators

If split flavors are used, Nova needs to be patched to ignore the usual quotas for instances of flavors with the `quota:separate = "true"` extra spec.
Instead, Nova must accept quotas with the same naming pattern (`instances_$FLAVOR`), and only enforce these quotas when accepting new instances using Ironic flavors, without counting those instances towards the usual quotas.
In SAP Cloud Infrastructure, Nova carries a custom patch set that implements this behavior.

### OS type inference

On instance subresources, the `os_type` indicates which OS is likely running on the instance.
This is intended to be used for billing of OS licenses.

For instances booted from an image, the image metadata is inspected in Glance.
The `os_type` is: (in order of priority)

- `image-unknown`, if no valid image reference exists in the instance metadata
- `image-deleted`, if the image has been deleted since the instance was booted
- the value in the `vmware_ostype` attribute on the image metadata, if that field exists and the value is valid
- `$TYPE`, if the image metadata contains a tag of the form `ostype:$TYPE`
- `unknown`, if no other rule matches

For instances booted using a Cinder volume as root disk, the volume metadata is inspected in Cinder by looking for volume attachment to `/dev/sda` or `/dev/vda`.
The `os_type` is: (in order of priority)

- `rootdisk-missing`, if the boot volume has an empty ID
- `rootdisk-inspect-error`, if the boot volume cannot be located or if its metadata cannot be inspected in Glance
- the value in the `volume_image_metadata.vmware_ostype` attribute on the volume metadata, if that field exists and the value is valid
- `unknown`, if no other rule matches

## Capacity calculation

TODO

### Split flavors

TODO
