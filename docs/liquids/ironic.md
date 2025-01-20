# Liquid: `ironic`

This liquid provides support for the baremetal compute service Ironic.

- The suggested service type is `liquid-ironic`.
- The suggested area is `compute`.

## Service-specific configuration

| Field | Type | Description |
| ----- | ---- | ----------- |
| `with_subcapacities` | boolean | If true, subcapacities are reported. |
| `with_subresources` | boolean | If true, subresources are reported. |
| `node_page_limit` | integer | When listing baremetal nodes during capacity calculation, only this many nodes will be listed at once. Defaults to 100. This number has a major impact on the peak memory usage of this liquid, because Ironic nodes often have an absurd amount of metadata on them that we need to parse temporarily. Reducing this number reduces memory consumption nearly linearly, at the cost of linearly increasing the amount of requests that need to be made to Ironic. |

## Resources

There is one resource for each Nova flavor that is used for baremetal deployments using nodes managed by Ironic:

| Resource | Unit | Capabilities |
| -------- | ---- | ------------ |
| `instances_$FLAVOR` | None | HasCapacity = true, HasQuota = true |

Each of these resources carries the following attributes:

| Field | Type | Description |
| ----- | ---- | ----------- |
| `attributes.cores` | integer | Number of CPU cores in this flavor. |
| `attributes.ram_mib` | integer | Amount of RAM in this flavor in MiB. |
| `attributes.disk_gib` | integer | Amount of local disk in this flavor in GiB. |

If `with_subresources` is set, each `instances_$FLAVOR` resource will have one subresource for each instance of that flavor, with the following fields:

| Field | Type | Description |
| ----- | ---- | ----------- |
| `id` | string | The UUID of the Nova instance. |
| `name` | string | The human-readable name of the Nova instance. |
| `attributes.status` | string | The status of the instance according to Nova. |
| `attributes.metadata` | object of strings | User-supplied key-value data on this instance according to Nova. |
| `attributes.tags` | array of strings | User-supplied tags on this instance according to Nova. |
| `attributes.os_type` | string | The OS type, as inferred from the image that was used to boot this instance. |

The logic for `os_type` inference is shared with liquid-nova, and is explained [in the documentation for liquid-nova](./nova.md#os-type-inference).

### Considerations for cloud operators

This liquid will consider all flavors that have the extra spec `capabilities:hypervisor_type = "ironic"` in Nova.
You need to make sure that the extra specs on your Ironic flavors are all set up in this way.

Furthermore, Nova needs to be patched to ignore the usual quotas for instances of Ironic flavors.
Instead, Nova must accept quotas with the same naming pattern (`instances_$FLAVOR`), and only enforce these quotas when accepting new instances using Ironic flavors, without counting those instances towards the usual quotas.
In SAP Cloud Infrastructure, Nova carries a custom patch set that triggers this behavior on presence of the `quota:instance_only` and `quota:separate` extra specs.

## Capacity calculation

If `with_subcapacities` is set, each `instances_$FLAVOR` resource will have one subcapacity for each matching baremetal server, with the following fields:

| Field | Type | Description |
| ----- | ---- | ----------- |
| `id` | string | The UUID of the Ironic node. |
| `name` | string | The human-readable name (usually the hostname) of this node in Ironic. |
| `attributes.provision_state` | string | The `provision_state` attribute of this node in Ironic. |
| `attributes.target_provision_state` | string | The `target_provision_state` attribute of this node in Ironic, if any. |
| `attributes.serial_number` | string | The `properties.serial` attribute of this node in Ironic, if any. |
| `attributes.instance_id` | string | The UUID of the Nova instance running on this node, if any. |

### Considerations for cloud operators

Capacity for each baremetal flavor is counted by finding Ironic nodes that have the flavor name as their `resource_class`.
You need to make sure that your resource classes are set up in this way in the Placement API.
