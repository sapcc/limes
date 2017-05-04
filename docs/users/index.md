# Documentation for Limes users

Limes is a quota and usage tracking service. If Limes is deployed in an OpenStack clusters, new domains and projects
start out with zero quota unless someone with the required permissions approves quota for them. (There may be exceptions
for certain auto-created resources, e.g. the `default` security group in Neutron.)

## Available clients

* At the time of this writing, there is no command-line client for Limes. You can send requests to
  [the HTTP API](./api-v1-specification.md) directly, as shown [in this guide](./api-example.md).
* The OpenStack web dashboard [Elektra](https://github.com/sapcc/elektra) contains an optional *Resource Management*
  module that becomes accessible if Limes is deployed in the target OpenStack cluster.

## Timing of automatic processes

* For each project, quota and usage data will be scraped from each backend service into Limes' database every **30
  minutes**, or when a user requests an immediate sync via the API. When displaying project data on the API, the time of
  the last scrape event will be indicated by the `scraped_at` field.
* For each cluster, capacity data is scraped into Limes' database every **15 minutes**.

If updated project quotas are not reflected in the backend service, you can try to request an immediate sync via the API
or in your client (e.g. via Elektra's "Sync Now" button). Whenever quota is scraped from the backend service, Limes will
try to enforce its own quota values in the backend service if the backend quotas diverge.
