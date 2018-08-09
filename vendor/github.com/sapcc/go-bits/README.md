# go-bits

Some tiny pieces of Go code, extracted from their original applications for
reusability. Each subdirectory is its own individual package. Feel free to add
to this.

* [gopherpolicy](./gopherpolicy) integrates [Gophercloud](https://github.com/gophercloud/gophercloud) with [goslo.policy](https://github.com/databus23/goslo.policy), for OpenStack services that need to validate client tokens and check permissions.
* [logg](./logg) adds some convenience functions to [log](https://golang.org/pkg/log/).
* [respondwith](./respondwith) contains some helper functions for generating responses in HTTP handlers.
* [retry](./retry) contains helper methods for creating retry loops using different strategies.
