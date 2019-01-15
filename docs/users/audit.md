# Audit trail

Limes records all events (read: quota changes) at the domain and project level in an Open Standards [CADF format](https://www.dmtf.org/standards/cadf). The log can be used by information auditors or cloud based audit APIs to track events for a resource in a domain or project.

Refer to the [configuration guide](../operators/config.md#audit-trail) for audit trail configuration options.

---

For any given event, the log contains the following details: What, When, Who, From Where, On What, Where, To Where of an activity<sup>*</sup>. This is also referred to as the 7 W’s of audit and compliance.

&ast;*an activity is a type of event that provides information about actions having occurred or intended to occur, and initiated by some resource or done against some resource.*

### The 7 “W”s of audit

| “W” Component | CADF Mandatory Properties  | CADF Optional Properties (where applicable) | Description |
| --- | --- | --- | --- |
| What | `event.action`<br>`event.outcome`<br>`event.eventType` | `event.reason` | “what” activity occurred; “what” was the result. |
| When | `event.eventTime` || “when” did it happen. |
| Who | `event.initiator.id`<br>`event.initiator.typeURI` | `event.initiator.name` | “who” (person or service) initiated the action. |
| FromWhere || `event.initiator.host`<br>`event.initiator.domain`<br>`event.initiator.domain_id`<br>`event.initiator.project_id` | "FromWhere" provides information describing where the action was initiated from. |
| OnWhat | `event.target.id`<br>`event.target.typeURI`  | `event.target.domain_id`<br>`event.target.project_id` | “onWhat” resource did the activity target. |
| Where | `event.observer.id`<br>`event.observer.typeURI` | `event.observer.name` | “where” did the activity get observed (reported), or modified in some way. |
| ToWhere ||| "ToWhere" provides information describing where the target resource that is affected by the action is located. |

---

### Example audit event

```json
{
  "typeURI": "http://schemas.dmtf.org/cloud/audit/1.0/event",
  "id": "3e2a61f2-c25a-4167-be17-d4e82907460e",
  "eventTime": "2018-07-26T14:18:41.877636+00:00",
  "eventType": "activity",
  "action": "update",
  "outcome": "success",
  "reason": {
    "reasonType": "HTTP",
    "reasonCode": "200"
  },
  "initiator": {
    "typeURI": "service/security/account/user",
    "name": "example-username",
    "id": "example-userid",
    "domain": "example-domain",
    "domain_id": "617c0987-5899-4fda-923a-7d86f682e62d",
    "project_id": "0733265f-5f6a-4aa9-a727-06fbb021e79e",
    "host": {
      "address": "::1",
      "agent": "curl/7.54.0"
    }
  },
  "target": {
    "typeURI": "service/compute/ram/quota",
    "id": "example-project-id",
    "attachments": [
      {
        "name": "payload",
        "typeURI": "mime:application/json",
        "content": "{\"oldQuota\":10248,\"newQuota\":13000,\"unit\":MiB}"
      }
    ],
    "project_id": "example-project-id",
    "domain_id": "example-domain-id"
  },
  "observer": {
    "typeURI": "service/resources",
    "name": "limes",
    "id": "82d7120c-a5aa-461e-bd33-cde46cba8fdc"
  },
  "requestPath": "/v1/domains/example-domain-id/projects/example-project-id"
}
```

### Event field mapping in Limes nomenclature

The table below should help you understand what the different fields in an audit event mean.

| Field | Description |
| --- | --- |
| `event.typeURI` | CADF event schema. |
| `event.id` | Event UUID. |
| `event.eventTime` | Time at which the quota was changed. |
| `event.eventType` | Defaults to activity (refer to section 4.5.1 of [CADF spec][cadf-spec]). |
| `event.action` | Defaults to update (refer to section A.3.5 of [CADF spec][cadf-spec]). |
| `event.outcome` | `success` or `failure` with respect to the `action`. |
| `event.reason.reasonType` | Defaults to HTTP. |
| `event.reason.reasonCode` | Appropriate HTTP status code depending on the `outcome`. |
| `event.initiator.typeURI` | Defaults to `service/security/account/user`. |
| `event.initiator.name` | Username of the person who changed the quota. |
| `event.initiator.id` | User ID of the person who changed the quota. |
| `event.initiator.domain` | Name of domain where the initiating user was authorized. |
| `event.initiator.domain_id` | ID of domain where the initiating user was authorized. |
| `event.initiator.project_id` | ID of project where the initiating user was authorized. |
| `event.initiator.host.address` | Host's address from where the quota change was requested. |
| `event.initiator.host.agent` | Curl or Elektron depending on where the request came from. |
| `event.target.typeURI` | *Which* quota was changed. |
| `event.target.id` | Project or Domain ID, depending on *where* the quota was changed. |
| `event.target.attachments` | Additional info regarding the specific quota change. |
| `event.target.project_id` | ID of project where the quota was changed. |
| `event.target.domain_id` | ID of domain where the quota was changed. |
| `event.target.oldQuota` | Quota of the resource before the change. |
| `event.target.newQuota` | New quota of the resource after the change. |
| `event.target.unit` | Unit of the resource that was changed, if it is measured in a certain unit. |
| `event.observer.typeURI` | Defaults to `service/resources`. |
| `event.observer.name` | Defaults to Limes. |
| `event.observer.id` | UUID for Limes generated at its startup. |
| `event.requestPath` | API request path. |

[cadf-spec]: https://www.dmtf.org/sites/default/files/standards/documents/DSP0262_1.0.0.pdf
