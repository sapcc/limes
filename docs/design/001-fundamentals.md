# Fundamental requirements

- expose OpenStack-like REST API
- scheduled background tasks to:
  - scrape quota/usage data from backing services (once per hour for each project)
  - dump quota/usage data into Swift as a machine-readable file (once per hour, for consumption by billing team)
- support plugins that implement quota/usage/capacity scraping per OpenStack service (Nova, Neutron, etc.)
- expose all quota/usage/capacity data as Prometheus metrics

# Basic structure

Multiple processes, talking to the same Postgres database:

- API service: exposes Limes API, performs synchronous requests (domain/project discovery)
- collector service: performs asynchronous jobs (capacity scanning, quota/usage scraping)
- Prometheus exporter: can be implemented inside the API service or split into a separate process

The collector service consists of plugins that implement capacity scanning and
quota/usage scraping for each supported backing service (Nova, Cinder, etc.).
Each collector runs in a separate thread.

## Usecase: Shared services

Limes includes support for services that are shared across *OpenStack clusters* (i.e. separate OpenStack installations
with separate service catalogs). In this case, multiple Limes installationsi (one per cluster) will share the same
Postgres database, but use different *cluster IDs* to identify their cluster's data within the database.

A *shared service* is a backend service which is available in multiple clusters. For example, a Swift object storage
setup can have multiple proxy deployments which each authenticate against a different cluster's Keystone. In this case,
the total capacity which is reported by the shared service needs to be distributed among all clusters using the shared
service.

When one of the cluster does not use Limes only for the shared service, not for its local resources, Limes can be
configured to only collect and manage the resources provided by shared services.
