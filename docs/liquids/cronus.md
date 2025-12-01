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

| Rates                       | Unit | Capabilities    |
|-----------------------------|------|-----------------|
| `attachment_size`           | `B`  | HasUsage = true |
| `data_transfer_in`          | `B`  | HasUsage = true |
| `data_transfer_out`         | `B`  | HasUsage = true |
| `recipients`                | None | HasUsage = true |
| `messages_sent_aws`         | None | HasUsage = true |
| `messages_received_aws`     | None | HasUsage = true |
| `data_sent_aws`             | `B`  | HasUsage = true |
| `data_received_aws`         | `B`  | HasUsage = true |
| `messages_sent_postfix`     | None | HasUsage = true |
| `messages_received_postfix` | None | HasUsage = true |
| `data_sent_postfix`         | `B`  | HasUsage = true |
| `data_received_postfix`     | `B`  | HasUsage = true |


