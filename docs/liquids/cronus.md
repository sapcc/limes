<!--
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

# Liquid: `cronus`

This liquid provides support for the email service Cronus (SAP Converged Cloud internal only).

- The suggested service type is `liquid-cronus`.
- The suggested area is `email`.

## Service-specific configuration

None.

## Rates

| Rates                  | Unit | Capabilities    |
| ---------------------- | ---- | --------------- |
| `attachment_size`      | `B`  | HasUsage = true |
| `data_transfer_in`     | `B`  | HasUsage = true |
| `data_transfer_out`    | `B`  | HasUsage = true |
| `recipients`           | None | HasUsage = true |
