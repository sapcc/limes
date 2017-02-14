# Fundamental requirements

- expose OpenStack-like REST API
- scheduled background tasks to:
  - scrape quota/usage data from backing services (once per hour for each project)
  - dump quota/usage data into Swift as a machine-readable file (once per hour, for consumption by billing team)
- support plugins that implement quota/usage/capacity scraping per OpenStack service (Nova, Neutron, etc.)
- expose all quota/usage/capacity data as Prometheus metrics

## Basic structure

Multiple processes, talking to the same Postgres database:

- API service: exposes Limes API, performs synchronous requests (domain/project discovery)
- collector service: performs asynchronous jobs (capacity scanning, quota/usage scraping)
- Prometheus exporter: can be implemented inside the API service or split into a separate process

The collector service consists of plugins that implement capacity scanning and
quota/usage scraping for each supported backing service (Nova, Cinder, etc.).
Each collector runs in a separate thread.

## Shared Service usecase
If a service is shared across OpenStack clusters, there would be an addtional deployment of the collector service
with credentials to the keystone backend. A config option would allow to scrape only information for a concrete 
service.
TODO: Do the collectors share a common database?
