<!--
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

# Liquid: `swift`

This liquid provides support for the object storage service Swift.

- The suggested service type is `liquid-swift`.
- The suggested area is `storage`.

## Service-specific configuration

| Field | Type | Description |
| --- | --- | --- |
| `rate_display_names` | object of strings | If provided, causes the liquid to declare one limit-only rate per entry in this object, with the key as the rate name and the value as the display name. This is a facility to afford testing of limit-only rates in QA scenarios and may be removed at any time. |

## Resources

| Resource | Unit | Capabilities |
| --- | --- | --- |
| `capacity` | Bytes | HasCapacity = false, HasQuota = true |
