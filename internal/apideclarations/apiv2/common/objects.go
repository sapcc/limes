// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package common

// DomainMetadata contains the metadata for a domain.
// It appears in types [resourcesv2.DomainReport] and [ratesv2.DomainReport].
type DomainMetadata struct {
	// UUID is what OpenStack commonly refers to as domain_id.
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

// ProjectMetadata contains the metadata for a project.
// It appears in types [resourcesv2.ProjectReport] and [ratesv2.ProjectReport].
type ProjectMetadata struct {
	// UUID is what OpenStack commonly refers to as project_id.
	UUID string `json:"uuid"`
	Name string `json:"name"`
	// The ParentUUID is the ID of the domain in case the project is not a subproject,
	// else it is the UUID of another project. This hierarchy can have multiple levels.
	ParentUUID string         `json:"parent_uuid"`
	DomainInfo DomainMetadata `json:"domain"`
}
