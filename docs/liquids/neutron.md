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
Each resource will only be provided if Neutron advertises it in the default quota set and if it is accepted by this liquid.
Accepted resources are hardcoded in the liquid (see list below).

| Resource                 | Unit | Capabilities                         |
| ------------------------ | ---- | ------------------------------------ |
| `floating_ips`           | None | HasCapacity = false, HasQuota = true |
| `networks`               | None | HasCapacity = false, HasQuota = true |
| `ports`                  | None | HasCapacity = false, HasQuota = true |
| `rbac_policies`          | None | HasCapacity = false, HasQuota = true |
| `routers`                | None | HasCapacity = false, HasQuota = true |
| `security_group_rules`   | None | HasCapacity = false, HasQuota = true |
| `security_groups`        | None | HasCapacity = false, HasQuota = true |
| `subnet_pools`           | None | HasCapacity = false, HasQuota = true |
| `subnets`                | None | HasCapacity = false, HasQuota = true |
| `bgpvpns`                | None | HasCapacity = false, HasQuota = true |
| `trunks`                 | None | HasCapacity = false, HasQuota = true |
| `endpoint_groups`        | None | HasCapacity = false, HasQuota = true |
| `ikepolicies`            | None | HasCapacity = false, HasQuota = true |
| `ipsec_site_connections` | None | HasCapacity = false, HasQuota = true |
| `ipsecpolicies`          | None | HasCapacity = false, HasQuota = true |
| `vpnservices`            | None | HasCapacity = false, HasQuota = true |
| `firewall_groups`        | None | HasCapacity = false, HasQuota = true |
| `firewall_policies`      | None | HasCapacity = false, HasQuota = true |
| `firewall_rules`         | None | HasCapacity = false, HasQuota = true |
| `routers_flavor_$NAME`   | None | HasCapacity = false, HasQuota = true (SAP-specific extension) |

If there is a `$NAME` placeholder, there will be a resource for any quota that is advertised by Neutron with a matching name.
