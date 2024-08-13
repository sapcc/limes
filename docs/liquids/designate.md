# Liquid: `designate`

This liquid provides support for the DNS service Designate.

- The suggested service type is `liquid-designate`.
- The suggested area is `network`.

## Service-specific configuration

None.

## Resources

| Resource               | Unit | Capabilities                         |
| ---------------------- | ---- | ------------------------------------ |
| `zones`                | None | HasCapacity = false, HasQuota = true |
| `recordsets_per_zone`  | None | HasCapacity = false, HasQuota = true |

When the `recordsets_per_zone` quota is set, the backend quota for records per zone is set to 20 times that value, to
fit into the `records_per_recordset` quota (which is set to 20 by default in Designate). The quota for records per zone
cannot be managed explicitly in this liquid.
