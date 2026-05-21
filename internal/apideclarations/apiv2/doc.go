// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

// Package apiv2 provides the specification for the [Limes] /v2 API.
//
// # Concepts
// TODO: copy this section from v1 API when the API is finished
//
// # Common Request Headers
// **X-Auth-Token**: As with all OpenStack services, this header must always contain a [Keystone token](https://docs.openstack.org/api-ref/identity/v3/index.html#password-authentication-with-scoped-authorization).
// In the /v2 API, the token often determines the scope of the data you get back from the API.
// I.e. when you provide a project-scoped token, you get back data for that project.
// When you provide a domain-scoped token, you get back data for that domain.
// When you provide a cloud-admin-scoped token, you get back all data.
// Unscoped tokens are not supported.
//
// # Common Query Arguments
// TODO: fill when implemented
//
// # Endpoints
// ## GET /resources/v2/info
// Returns information about the clusters resources.
// Authorization: Requires a token that matches the "v2:cluster:info" rule.
// **On success**: Returns an object of type [resourcesv2.InfoReport].
// **On failure**: Returns an error string with an appropriate HTTP status code.
//
// ## GET /resources/v2/cluster
// Returns the resource data for the whole cluster.
// Authorization: Requires a token that matches the "v2:cluster:report" rule.
// Supported params: [paramsv2.WithSubcapacities]
// **On success**: Returns an object of type [resourcesv2.ClusterReport].
// **On failure**: Returns an error string with an appropriate HTTP status code.
//
// ## GET /resources/v2/domains(/:domain_id)?
// Returns the resource data for one or multiple domains.
// Authorization: Requires a token that matches the "v2:domain:report" rule.
// **On success**: Returns an object of type [resourcesv2.DomainReport].
// **On failure**: Returns an error string with an appropriate HTTP status code.
//
// ## GET /resources/v2/projects(/:project_id)?
// Returns the resource data for one or multiple projects.
// Authorization: Requires a token that matches the "v2:project:report" rule.
// **On success**: Returns an object of type [resourcesv2.ProjectReport].
// **On failure**: Returns an error string with an appropriate HTTP status code.
//
// ## GET /resources/v2/availability
// TODO: fill when implemented
//
// ## GET /rates/v2/info
// Returns information about the clusters rates.
// Authorization: Requires a token that matches the "v2:cluster:info" rule.
// **On success**: Returns an object of type [ratesv2.InfoReport].
// **On failure**: Returns an error string with an appropriate HTTP status code.
//
// ## GET /rates/v2/cluster
// Returns the rate data for the whole cluster.
// Authorization: Requires a token that matches the "v2:cluster:report" rule.
// **On success**: Returns an object of type [ratesv2.ClusterReport].
// **On failure**: Returns an error string with an appropriate HTTP status code.
//
// ## GET /rates/v2/domains(/:domain_id)?
// Returns the rate data for one or multiple domains.
// Authorization: Requires a token that matches the "v2:domain:report" rule.
// **On success**: Returns an object of type [ratesv2.DomainReport].
// **On failure**: Returns an error string with an appropriate HTTP status code.
//
// ## GET /rates/v2/projects(/:project_id)?
// Returns the rate data for one or multiple projects.
// Authorization: Requires a token that matches the "v2:project:report" rule.
// **On success**: Returns an object of type [ratesv2.ProjectReport].
// **On failure**: Returns an error string with an appropriate HTTP status code.
//
// [Limes]: https://github.com/sapcc/limes
package apiv2
