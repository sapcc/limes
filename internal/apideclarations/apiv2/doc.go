// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

// Package apiv2 contains the specification for the v2 API of [Limes], an OpenStack service for managing quotas as well as usage and capacity data.
//
// # Concepts
//
// TODO: copy this section from v1 API and/or the LIQUID API when the API is finished
//
// # API structure
//
// The Limes v2 API is structured as a REST-like HTTP API akin to those of the various OpenStack services.
// Like with any other OpenStack API, clients authenticate by providing their [Keystone token] in the HTTP header "X-Auth-Token".
// Requests without a valid token will be rejected with status 401 (Unauthorized).
// Requests with a valid token that confers insufficient access will be rejected with status 403 (Forbidden).
//
// Some endpoints will provide different information based on the scope of the token, as documented in the section for the respective endpoint.
// In this context, a "cloud-admin token" refers to whatever scope the administrator of the respective OpenStack has designated as cloud-admin scope.
// This may either mean a system-scoped token, or a token scoped to a certain well-known cloud-admin project.
//
// Each individual endpoint is documented in a section of this docstring whose title starts with "Endpoint:".
// Unless noted otherwise, a liquid must implement all documented endpoints.
// The full URL of the endpoint is obtained by appending the subpath from the section header to the liquid's base URL from the Keystone service catalog.
//
// The documentation for an endpoint may refer to a request body being expected or a response body being generated on success.
// In all such cases, the request or response body will be encoded as "Content-Type: application/json".
// The structure of the payload must conform to how the referenced Go type would be serialized by the Go standard library's "encoding/json" package.
//
// When producing a successful response, the status code shall be 200 (OK) unless noted otherwise.
// When producing an error response (with a status code between 400 and 599), the liquid shall include a response body of "Content-Type: text/plain" to indicate the error.
//
// # Common query arguments
//
// TODO: fill when implemented
//
// # Endpoint: GET /resources/v2/info
//
// Returns information about the cluster's resources, potentially limited to those resources that are accessible within the authenticated scope:
// A project-scoped token will receive information about all resources available in that project.
// A domain-scoped token will receive information about all resources available in at least one project of that domain.
// A cloud-admin token will receive information about all resources.
//   - On success, the response body payload will be of type [resourcesv2.InfoReport].
//
// # Endpoint: GET /resources/v2/cluster
//
// TODO: fill when implemented
//
// # Endpoint: GET /resources/v2/domains(/:domain_id)?
//
// TODO: fill when implemented
//
// # Endpoint: GET /resources/v2/projects(/:project_id)?
//
// TODO: fill when implemented
//
// # Endpoint: GET /resources/v2/availability
//
// TODO: fill when implemented
//
// # Endpoint: GET /rates/v2/info
//
// Returns information about the cluster's rates, potentially limited to those rates that are accessible within the authenticated scope:
// A project-scoped token will receive information about all rates available in that project.
// A domain-scoped token will receive information about all rates available in at least one project of that domain.
// A cloud-admin token will receive information about all rates.
//   - On success, the response body payload will be of type [ratesv2.InfoReport].
//
// # Endpoint: GET /rates/v2/cluster
//
// TODO: fill when implemented
//
// # Endpoint: GET /rates/v2/domains(/:domain_id)?
//
// TODO: fill when implemented
//
// # Endpoint: GET /rates/v2/projects(/:project_id)?
//
// TODO: fill when implemented
//
// [Keystone token]: https://docs.openstack.org/api-ref/identity/v3/index.html#password-authentication-with-scoped-authorization
// [Limes]: https://github.com/sapcc/limes
package apiv2

import (
	ratesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/rates"
	resourcesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/resources"
)

var (
	// need to make sure these packages are imported for docstring links to work
	_ = resourcesv2.InfoReport{}
	_ = ratesv2.InfoReport{}
)
