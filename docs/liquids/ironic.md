# Liquid: `ironic`

This liquid provides support for the baremetal compute service Ironic.

- The suggested service type is `liquid-ironic`.
- The suggested area is `compute`.

## Service-specific configuration

| Field | Type | Description |
| ----- | ---- | ----------- |
| `with_subcapacities` | boolean | If true, subcapacities are reported. |
| `with_subresources` | boolean | If true, subresources are reported. |

## Resources

There is one resource for each Nova flavor that is used for baremetal deployments using nodes managed by Ironic:

| Resource | Unit | Capabilities |
| -------- | ---- | ------------ |
| `instances_$FLAVOR` | None | HasCapacity = true, HasQuota = true |

If `with_subresources` is set, each `instances_$FLAVOR` resource will have one subresource for each instance of that flavor, with the following fields:

| Field | Type | Description |
| ----- | ---- | ----------- |
| `id` | string | The UUID of the Nova instance. |
| `name` | string | The human-readable name of the Nova instance. |
| `attributes.status` | string | The status of the instance according to Nova. |
| `attributes.tags` | array of strings | The tags on this instance according to Nova. |
| `attributes.cores` | integer | Number of CPU cores in this flavor. |
| `attributes.ram_mib` | integer | Amount of RAM in this flavor in MiB. |
| `attributes.disk_gib` | integer | Amount of local disk in this flavor in GiB. |
| `attributes.os_type` | string | The OS type, as inferred from the image that was used to boot this instance. |

TODO: `os_type` inference is shared with Nova. When the Nova subresource scraping is moved to LIQUID, the method shall be documented over there, and a backreference shall be added here.

### Considerations for cloud operators

This liquid will consider all flavors that have the extra spec `capabilities:hypervisor_type = "ironic"` in Nova.
You need to make sure that the extra specs on your Ironic flavors are all set up as described above.

Furthermore, Nova needs to be patched to ignore the usual quotas for instances of Ironic flavors.
Instead, Nova must accept quotas with the same naming pattern (`instances_$FLAVOR`), and only enforce these quotas when accepting new instances using Ironic flavors, without counting those instances towards the usual quotas.
In SAP Converged Cloud, Nova carries a custom patch set that triggers this behavior on presence of the `quota:instance_only` and `quota:separate` extra specs.

## Capacity calculation

TODO

If `with_subcapacities` is set, each `instances_$FLAVOR` resource will have one subcapacity for each matching baremetal server, with the following fields:

| Field | Type | Description |
| ----- | ---- | ----------- |
| TODO | | |
