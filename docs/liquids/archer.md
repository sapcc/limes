<!--
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

# Liquid: `archer`

This liquid provides support for the endpoint injection service [Archer](https://github.com/sapcc/archer).

- The suggested service type is `liquid-archer`.
- The suggested area is `network`.

## Service-specific configuration

None.

## Resources

| Resource    | Unit | Capabilities                         |
| ----------- | ---- | ------------------------------------ |
| `endpoints` | None | HasCapacity = false, HasQuota = true |
| `services`  | None | HasCapacity = false, HasQuota = true |
