<!--
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

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
| `hypervisor_selection.aggregate_name_pattern` | regexp | Only match hypervisors that reside in an aggregate matching this pattern. If a hypervisor resides in multiple matching aggregates, an error is raised. It is recommended to use AZ-wide aggregates here. |
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

If `with_subresources` is set, each `instances` or `instances_${FLAVOR_NAME}` resource will have one subresource for each instance of the respective flavor(s), with the following fields:

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

**TODO:** There is incomplete pre-alpha-level support for `hw_version`-separated pooled quotas, which is not documented here yet until the implementation is completed.

### Considerations for cloud operators

If split flavors are used, Nova needs to be patched to ignore the usual quotas for instances of flavors with the `quota:separate = "true"` extra spec.
Instead, Nova must accept quotas with the same naming pattern (`instances_${FLAVOR_NAME}`), and only enforce these quotas when accepting new instances using Ironic flavors, without counting those instances towards the usual quotas.
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

On the most basic level, pooled capacity is calculated by enumerating Nova hypervisors matching the configured `hypervisor_selection` and taking a sum over their total capacity:

| Resource | Method |
| --- | --- |
| `compute/cores` | The sum of the reported CPUs for all matching hypervisors. Note that the hypervisor statistics reported by Nova do not take overcommit into account, so you may have to configure the overcommitment again in Limes for accurate capacity reporting. |
| `compute/instances` | Estimated per AZ as `10000 * count(matchingAggregates)`, but never more than `sumLocalDisk / maxDisk`, where `sumLocalDisk` is the sum of the local disk size for all matching hypervisors, and `maxDisk` is the largest disk requirement of all pooled flavors matching the configured `flavor_selection`. |
| `compute/ram` | The sum of the reported RAM for all matching hypervisors. |

If `with_subcapacities` is set, each of those three resources will have one subcapacity for each Nova hypervisor, with the following fields:

| Field | Type | Description |
| --- | --- | --- |
| `name` | string | The `service.host` attribute of the hypervisor according to Nova. |
| `capacity` | unsigned integer | How much this hypervisor contributes to the resource's overall capacity value. |
| `usage` | unsigned integer | How much this hypervisor contributes to the resource's overall usage value. |
| `attributes.aggregate_name` | string | Which aggregate was matched and used to establish an AZ association. |
| `attributes.traits` | list of strings | The traits of this hypervisor's resource provider according to the Placement API. |

**Warning:** The subcapacities currently do not include entries for hypervisors that only host split flavors and no pooled flavors.
This is considered a bug and may be fixed at a later time.

### Split flavors

If there are split flavors (as defined above), the capacity for `compute/instances_${FLAVOR_NAME}` eats into the pooled capacity.
For example, if a split flavor named `foo` with 32 vCPUs is reported with a capacity of 5 instances, then the `compute/cores` capacity is reduced by `5 * 32 = 160` (and analogously for `compute/instances` and `compute/ram`).

Capacity calculation is not as straight-forward as for pooled resources:
Nova and Placement only tell us about the size of the hypervisors in terms of CPU, RAM and local disk.
But the capacity needs to be reported in terms of number of instances that can be deployed per flavor.
There is no single correct answer to this because different flavors use different amounts of resources.

Our calculation algorithm takes existing usage and confirmed commitments, as well as commitments that are waiting to be confirmed,
for all split-flavor resources and simulates placing those existing and requested instances onto the matching hypervisors.
Afterwards, any remaining space is filled up by following the existing distribution of flavors as closely as possible.
The resulting capacity is equal to how many instances could be placed in this simulation.
The placement simulation strives for an optimal result by using a binpacking algorithm.

When pooled flavors and split flavors can be placed on the same hypervisor,
demand of equal or higher priority for pooled flavors is blocked while trying to place demand for split flavors.
When filling up the remaining space with extra split-flavor instances like at the end of Option 2,
extra instances are only placed into the "fair share" of split flavors when compared with pooled flavors.
For example, if current demand of CPU, RAM and disk is 10% in split flavors and 30% in pooled flavors, that's a ratio of 1:3.
Therefore, split-flavor instances will be filled up to 25% of the total space to leave 75% for pooled flavors, thus matching this ratio.

#### A visual metaphor for capacity calculation

It can be helpful to think of the hypervisors as vessels that can contain solids or liquids.
The split-flavor instances have fixed sizes (according to their flavor configuration), so they behave like solid blocks.
The pooled resources do not have a fixed size, so they behave like a liquid that can fill the gaps between the solid blocks.
When simulating placement:

- We pour liquid into the hypervisor according to the usage for pooled resources.
  (Usage comes first because it is the highest-priority form of demand.)
- We then put solid blocks into the hypervisor according to the usage for split flavors,
  but only as long as the liquid does not overflow.
- All of this is then repeated for the other forms of demand, in decreasing order of priority:
  first unused commitments, then pending commitments.
- Finally, if any hypervisors are not full, we fill up with solid blocks and liquid at the same time until we cannot anymore.
