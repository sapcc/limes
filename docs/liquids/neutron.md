<!--
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

# Liquid: `neutron`

This liquid provides support for the networking service Neutron.

- The suggested service type is `liquid-neutron`.
- The suggested area is `network`.

## Service-specific configuration

None.

## Resources

Some of these resources are provided by optional Neutron extensions.
Each resource will only be provided if Neutron advertises it in the default quota set and if it is accepted by limes.
Accepted resources are hardcoded in limes (see list below).

| Resource               | Unit | Capabilities                         |
| ---------------------- | ---- | ------------------------------------ |
| `floating_ips`         | None | HasCapacity = false, HasQuota = true |
| `networks`             | None | HasCapacity = false, HasQuota = true |
| `ports`                | None | HasCapacity = false, HasQuota = true |
| `rbac_policies`        | None | HasCapacity = false, HasQuota = true |
| `routers`              | None | HasCapacity = false, HasQuota = true |
| `security_group_rules` | None | HasCapacity = false, HasQuota = true |
| `security_groups`      | None | HasCapacity = false, HasQuota = true |
| `subnet_pools`         | None | HasCapacity = false, HasQuota = true |
| `subnets`              | None | HasCapacity = false, HasQuota = true |
| `bgpvpns`              | None | HasCapacity = false, HasQuota = true |
| `trunks`               | None | HasCapacity = false, HasQuota = true |

In addition we also support dynamically registered Neutron resources.
These accepted based on a list of prefixes.
Currently this contains only `routers_flavor_`.
Note that this is a SAP-specific add-on.
