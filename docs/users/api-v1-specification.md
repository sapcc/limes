# Public API specification

Limes deals with two separate types of entities: **Resources** are strictly those things whose usage value refers to a
consumption at a specific point in time, and where quota is the upper limit on usage at each individual point in time.
In contrast, **rates** are all those things where the usage is accumulated over time, and there can be a limit of how
much usage can occur within a certain window of time.

The Limes API has therefore been split into two separately documented sub-specifications:

- [Resource API spec](./api-spec-resources.md)
- [Rate API spec](./api-spec-rates.md)
