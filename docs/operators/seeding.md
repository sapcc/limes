# Quota seeding

When Limes discovers a new domain or project for which a **quota seed** has been configured, the quotas in the seed are
immediately applied to that domain or project. This is particularly useful during build-up of a new OpenStack cluster to
assign quotas to the technical projects that are used by OpenStack services and auxiliary processes like monitoring and
logshipping.

## Caveats

The current implementation of seeding is geared towards supporting the buildup of new OpenStack clusters. Two
restrictions apply:

- Limes does not validate if the quota seed is internally consistent, i.e. if the project quotas per domain fit into
  that domain's quota.
- Project quota is only written into the backend when the `authoritative` flag is set in the cluster configuration.

## Configuration

Quota seeds are configured in a separate YAML file which is referenced in the `clusters.$id.seed` field of the main
configuration file. The seed configuration file looks like this:

```
domains:
  Default: # domain name
    object-store: # service type
      capacity: 1 TiB # resource name and quota value
  ...

projects:
  Default/swift-tests: # domain name + "/" + project name
    object-store: # service type
      capacity: 200 MiB # resource name and quota value
  ...
```

In this example, when Limes discovers the "Default" domain, it will assign 1 TiB of domain quota for the "capacity"
resource in the "object-store" service. Moreover, when Limes discovers the "swift-tests" project in said domain, it will
assign 200 MiB of project quota for the same resource.

For countable resources, the quota values are numbers matching the regex `[0-9]+`. For measured resources, the quota values:

- must match the regex `[0-9]+\s*[A-Za-z]+`, and
- the word at the end must be a unit name understood by Limes, and
- the value described by the full string must be an integer multiple of the base unit for that resource.

For example, RAM can only be allocated in MiB, so a hypothetical quota value of "2000 KiB" would be rejected since this
value lies between 1 MiB and 2 MiB.

All these criteria are checked when `limes collect` parses its configuration during startup, and any errors will
interrupt the collector and cause Limes to terminate immediately.
