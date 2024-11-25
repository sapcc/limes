# Liquid: `nova`

This liquid provides support for the compute service Nova.

- The suggested service type is `liquid-nova`.
- The suggested area is `compute`.

## Service-specific configuration

TODO

## Resources

TODO: Description

| Resource | Unit | Capabilities |
| --- | --- | --- |
| `cores` | None | HasCapacity = true, HasQuota = true |
| `ram` | MiB | HasCapacity = true, HasQuota = true |
| `instances` | None | HasCapacity = true, HasQuota = true |
| `server_groups` | None | HasCapacity = false, HasQuota = true |
| `server_group_members` | None | HasCapacity = false, HasQuota = true |
| `instances_$FLAVOR_NAME` | None | HasCapacity = true, HasQuota = true |

