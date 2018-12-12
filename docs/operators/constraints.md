# Quota constraints

Operators can configure **quota constraints** which Limes will enforce when discovering a new domain or project, and
whenever users request to change domain or project quota. Constraints are ordered by domain and project name, so this
facility is particularly useful when operators want to maintain quota for administrative projects that exist in multiple
OpenStack installations.

## Caveats

- Limes validates if the constraint set is internally consistent, i.e. if the minimum project quotas per domain fit into
  that domain's minimum quota. But it does **not** perform a similar validation when applying constraints to a new
  project. If the domain quota has already been given out to other projects not referenced in the constraint set, the
  domain quota may be overcommitted.
- Project quota is only written into the backend when the `authoritative` flag is set in the cluster configuration.

## Configuration

Quota constraints are configured in a separate YAML file which is referenced in the `clusters.$id.constraints` field of
the main configuration file. The constraint set looks like this:

```yaml
domains:
  Default: # domain name
    object-store: # service type
      capacity: at least 1 TiB, at most 5 TiB # resource name; and comma-separated list of operator and quota value
  ...

projects:
  Default/swift-tests: # domain name + "/" + project name
    object-store: # service type
      capacity: exactly 200 MiB # resource name; and comma-separated list of operator and quota value
  ...
```

In this example, when Limes discovers the "Default" domain, it will assign 1 TiB of domain quota for the "capacity"
resource in the "object-store" service to fulfil the "at least" constraint. Moreover, when Limes discovers the
"swift-tests" project in said domain, it will assign 200 MiB of project quota for the same resource.

Requests to change the quota for the "swift-tests" project will be rejected because of the "exactly" constraint.
Requests to change the quota for the "Default" domain will only be accepted if the requested domain quota is between
(inclusive) 1 and 5 TiB.

Constraints are parsed and validated when `limes collect` parses its configuration during startup, and any errors will
interrupt the collector and cause it to terminate immediately.

### Constraint syntax

Supported operators include "at least", "at most" and "exactly". For countable resources, the quota values are numbers
matching the regex `[0-9]+`. For measured resources, the quota values:

- must match the regex `[0-9]+\s*[A-Za-z]+`, and
- the word at the end must be a unit name understood by Limes, and
- the value described by the full string must be an integer multiple of the base unit for that resource.

For example, RAM can only be allocated in MiB, so a hypothetical quota value of "2000 KiB" would be rejected since this
value lies between 1 MiB and 2 MiB.

For domain constraints, a special operator "at least ... more than project constraints" is supported, that works like
"at least", but adds all "at least" and "exactly" constraints for projects in that domain.

```yaml
domains:
  Default:
    object-store:
      capacity: at least 1 TiB more than project constraints
      # This constraint gets compiled into "at least 7 TiB" because the
      # "at least" and "exactly" constraints for all projects in the "Default"
      # domain get added to it.

projects:
  Default/swift-tests:
    object-store:
      capacity: exactly 1 TiB
  Default/db-backups:
    object-store:
      capacity: at least 5 TiB
  customer-domain/webshop: # not considered because not in "Default" domain
    object-store:
      capacity: at least 1 TiB
```
