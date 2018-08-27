# go-bits

Some tiny pieces of Go code, extracted from their original applications for
reusability. Each subdirectory is its own individual package. Feel free to add
to this.

* [gopherpolicy](./gopherpolicy) integrates [Gophercloud](https://github.com/gophercloud/gophercloud) with [goslo.policy](https://github.com/databus23/goslo.policy), for OpenStack services that need to validate client tokens and check permissions.
* [logg](./logg) adds some convenience functions to [log](https://golang.org/pkg/log/).
* [postlite](./postlite) is a database library for applications that use PostgreSQL in production and SQLite for testing. It integrates [golang-migrate/migrate](https://github.com/golang-migrate/migrate) for data definition and imports the necessary SQL drivers.
* [respondwith](./respondwith) contains some helper functions for generating responses in HTTP handlers.
* [retry](./retry) contains helper methods for creating retry loops using different strategies.
