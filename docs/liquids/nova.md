# Liquid: `nova`

This liquid provides support for the compute service Nova.

- The suggested service type is `liquid-nova`.
- The suggested area is `compute`.

## Service-specific configuration

| Field | Type | Description |
| ----- | ---- | ----------- |
| `hypervisor_selection.hypervisor_type_pattern` | regexp | Only match hypervisors with a hypervisor_type attribute matching this pattern. |
| `hypervisor_selection.required_traits` | []string | Only those hypervisors will be considered whose resource providers have all of the traits without `!` prefix and none of those with `!` prefix. |
| `hypervisor_selection.shadowing_traits` | []string | If a hypervisor matches any of the rules in this configuration field (using the same logic as above for `required_traits`), the hypervisor will be considered shadowed. Its capacity will not be counted. |
| `hypervisor_selection.aggregate_name_pattern` | regexp | Only match hypervisors that reside in an aggregate matching this pattern. If a hypervisor resides in multiple matching aggregates, an error is raised. |
| `flavor_selection.required_extra_specs` | map[string]string | Only match flavors that have all of these extra specs. |
| `flavor_selection.excluded_extra_specs` | map[string]string | Exclude flavors that have any of these extra specs. |
| `with_subcapacities` | boolean | If true, subcapacities are reported. |
| `with_subresources` | boolean | If true, subresources are reported. |
| `binpack_behavior.score_ignores_cores`<br>`binpack_behavior.score_ignores_disk`<br>`binpack_behavior.score_ignores_ram` | boolean | If true, when ranking nodes during placement, do not include the respective dimension in the score. |
| `ignored_traits` | []string | Traits that should be ignored during confirmation that all pooled flavors agree on which trait-match extra specs they use.  |

## Resources

TODO: @majewsky please assist here

| Resource | Unit | Capabilities |
| --- | --- | --- |
| `cores` | None | HasCapacity = true, HasQuota = true |
| `ram` | MiB | HasCapacity = true, HasQuota = true |
| `instances` | None | HasCapacity = true, HasQuota = true |
| `server_groups` | None | HasCapacity = false, HasQuota = true |
| `server_group_members` | None | HasCapacity = false, HasQuota = true |
| `instances_$FLAVOR_NAME` | None | HasCapacity = true, HasQuota = true |

## Capacity calculation

TODO: @majewsky please assist here