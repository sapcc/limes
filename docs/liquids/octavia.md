<!--
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

# Liquid: `octavia`

This liquid provides support for the loadbalancing service Octavia.

- The suggested service type is `liquid-octavia`.
- The suggested area is `network`.

## Service-specific configuration

None.

## Resources

Each resource will only be provided if Octavia advertises it in the default quota set.

| Resource         | Unit | Capabilities                         |
| ---------------- | ---- | ------------------------------------ |
| `healthmonitors` | None | HasCapacity = false, HasQuota = true |
| `l7policies`     | None | HasCapacity = false, HasQuota = true |
| `listeners`      | None | HasCapacity = false, HasQuota = true |
| `loadbalancers`  | None | HasCapacity = false, HasQuota = true |
| `pool_members`   | None | HasCapacity = false, HasQuota = true |
| `pools`          | None | HasCapacity = false, HasQuota = true |
