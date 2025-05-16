<!--
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

# Builtin liquids

To integrate with other OpenStack services and manage their resources, Limes uses an API called [LIQUID (Limes Interface
for Quota and Usage Interrogation and Discovery)](https://pkg.go.dev/github.com/sapcc/go-api-declarations/liquid).
A component that implements LIQUID is called *a liquid* (lowercase). When deploying Limes, you need to make sure to
deploy a liquid for each OpenStack service whose resources Limes should manage.

If the service does not provide LIQUID support itself, you can use one of the liquids bundled with Limes:

- [`archer`](./archer.md) for the endpoint injection service [Archer](https://github.com/sapcc/archer)
- [`cinder`](./cinder.md) for the block storage service Cinder
- [`cronus`](./cronus.md) for the email service Cronus (SAP Converged Cloud internal only)
- [`designate`](./designate.md) for the DNS service Designate
- [`ironic`](./ironic.md) for the baremetal compute service Ironic
- [`manila`](./manila.md) for the shared file system storage service Manila
- [`neutron`](./neutron.md) for the networking service Neutron
- [`octavia`](./octavia.md) for the loadbalancing service Octavia
- [`swift`](./swift.md) for the object storage service Swift

## How to run

To run any of these liquids, use the command `limes liquid $NAME` (e.g. `limes liquid swift`). The following environment
variables need to be provided:

| Variable | Default | Description |
| --- | --- | --- |
| `LIQUID_CONFIG_PATH` | *(required)* | If the documentation of the liquid calls for service-specific configuration, the path to a JSON
configuration file must be passed in this variable. |
| `LIQUID_LISTEN_ADDRESS` | `:80` | Bind address for the HTTP API exposed by the liquid, e.g. `127.0.0.1:80` to bind only on one IP, or `:80` to bind on all interfaces and addresses. |
| `LIQUID_POLICY_PATH` | *(required)* | Path to the oslo.policy file that describes authorization behavior for this liquid. See below for details. |
| `OS_...` | *(required)* | A full set of OpenStack auth environment variables for the liquid's service user. See [documentation for openstackclient][osc] for details. |

In order for Limes to be able to find the liquid's API, it must be registered in the Keystone service catalog.
The documentation for each liquid provides a suggestion for what to put as the service type.
Finally, the `liquids` list in the Limes configuration must be extended with a entry referring to that liquid
(set `liquid_service_type` if not using the standard naming).

## Policy

Like most OpenStack services, these liquids use oslo.policy rules to describe authorization behavior. Please refer to
the [OpenStack documentation on policies][policy] for syntax reference. The following example policy from our productive
SAP Converged Cloud deployment shows which policy rules must be provided:

```json
{
    "readwrite": "role:cloud_resource_admin or (user_name:limes and user_domain_name:Default)",
    "readonly": "role:cloud_resource_viewer or rule:readwrite",

    "liquid:get_info": "rule:readonly",
    "liquid:get_capacity": "rule:readonly",
    "liquid:get_usage": "rule:readonly",
    "liquid:set_quota": "rule:readwrite"
}
```

The four `liquid:...` rules are the ones that are required. In this example, the liquids allow write access all human users
with the `cloud_resource_admin` role, as well as the specific technical user used by Limes. Read access is additionally
granted to all human users with the `cloud_resource_viewer` role.

Beyond what is shown in this example, rules for the project-scoped requests (`liquid:get_usage` and `liquid:set_quota`)
can use the object attribute `%(project_id)s` to refer to the project that the request targets.

[osc]:    https://docs.openstack.org/python-openstackclient/latest/cli/man/openstack.html
[policy]: https://docs.openstack.org/security-guide/identity/policies.html
