# Audit trail

### Work in progress

Limes records all events (read: quota changes) in an Open Standards [CADF formatted](https://www.dmtf.org/standards/cadf) log. The log can be used by information auditors or cloud based audit APIs to track events for a resource in a domain or project.

To-do: a link to the configuration options included in the `../operators/config.md`.

**Note:** Only the events at the domain and project level are *CADF formatted*.

For any given event, the log contains the following details: What, When, Who, From Where, On What, Where, To Where of an activity. This is also referred to as the 7 W’s of audit and compliance.

---

### Example Event

```json
{
  "typeURI": "http://schemas.dmtf.org/cloud/audit/1.0/event",
  "id": "example-event-id",
  "eventTime": "2018-07-05T15:13:19.098982+00:00",
  "eventType": "activity",
  "action": "update",
  "outcome": "success",
  "reason": {
    "reasonType": "HTTP",
    "reasonCode": "200"
  },
  "initiator": {
    "typeURI": "service/security/account/user",
    "name": "d07lorem36",
    "domain": "Default",
    "id": "64f737368b8e82asd45as641s4d1debb706438530fe6a45bfec26",
    "host": {
      "address": "0.0.0.0",
      "agent": "curl/7.54.0"
    },
    "project_id": "example"
  },
  "target": {
    "typeURI": "service/compute/ram/quota",
    "id": "example-target-id"
  },
  "observer": {
    "typeURI": "service/compute/ram",
    "name": "ram",
    "id": "example-observer-id"
  },
  "requestPath": "/v1/domains/example-domain-id/projects/example-project-id"
}
```

### The 7 “W”s of audit

| “W” Component | CADF Mandatory Properties  | CADF Optional Properties (where applicable) | Description |
| --- | --- | --- | --- |
| What | `event.action`<br>`event.outcome`<br>`event.eventType`  | `event.reason` | “what” activity occurred; “what” was the result. |
| When | `event.eventTime` || “when” did it happen. |
| Who | `event.initiator.id`<br>`event.initiator.typeURI` | `event.initiator.name`<br>`event.initiator.domain`<br>`event.initiator.project_id` | “who” (person or service) initiated the action. |
| FromWhere || `event.initiator.host` | "FromWhere" provides information describing where the action was initiated from. |
| OnWhat | `event.target.id`<br>`event.target.typeURI`  || “onWhat” resource did the activity target. |
| Where | `event.observer.id`<br>`event.observer.typeURI` | `event.observer.name` | “where” did the activity get observed (reported), or modified in some way. |
| ToWhere ||| "ToWhere" provides information describing where the target resource that is affected by the action is located. |

### Event field mapping in Limes nomenclature

| Field | Description |
| --- | --- |
| `event.typeURI` | CADF event schema |
| `event.id` | CADF generated event id |
| `event.eventTime` | time at which the quota was changed |
| `event.eventType` | defaults to activity (refer to section 4.5.1 of [CADF spec][cadf-spec]) |
| `event.action` | defaults to update (refer to section A.3.5 of [CADF spec][cadf-spec]) |
| `event.outcome` | whether the quota change was successful or not |
| `event.reason.reasonType` | defaults to HTTP (Limes only has a HTTP API at the moment) |
| `event.reason.reasonCode` | appropriate HTTP status code depending on the `event.outcome` |
| `event.initiator.typeURI` | to-do |
| `event.initiator.name` | to-do |
| `event.initiator.domain` | to-do |
| `event.initiator.id` | to-do |
| `event.initiator.host.address` | to-do |
| `event.initiator.host.agent` | curl or Elektron depending on where the request came from |
| `event.initiator.project_id` | project id |
| `event.target.typeURI` | which quota was changed |
| `event.target.id` | project or domain id, depending on where the quota was changed |
| `event.observer.typeURI` | which resource was affected by the change |
| `event.observer.name` | to-do |
| `event.observer.id` | to-do |
| `event.requestPath` | API request path |

[cadf-spec]: https://www.dmtf.org/sites/default/files/standards/documents/DSP0262_1.0.0.pdf
