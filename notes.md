# Design notes

Fundamental requirements:
- expose OpenStack-like REST API
- scheduled background tasks to:
  - scrape quota/usage data from backend services (once per hour for each project)
  - dump quota/usage data into Swift as a machine-readable file (once per hour, for consumption by billing team)
- support plugins that implement quota/usage/capacity scraping per OpenStack service (Nova, Neutron, etc.)
- expose all quota/usage/capacity data as Prometheus metrics
