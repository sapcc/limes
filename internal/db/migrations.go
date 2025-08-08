// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package db

import _ "embed"

//go:embed baseline.sql
var sqlBaseline string

var sqlMigrations = map[string]string{
	// NOTE: Migrations 1 through 61 have been rolled up into one at 2025-08-07
	// to better represent the current baseline of the DB schema.
	"061_rollup.up.sql": sqlBaseline,
	"061_rollup.down.sql": `
		DROP TRIGGER cluster_az_resources_check_path_values_trigger;
		DROP TRIGGER cluster_resources_check_path_values_trigger;
		DROP TRIGGER cluster_services_check_path_values_trigger;
		DROP FUNCTION check_path_values_trigger_function;
		DROP TRIGGER cluster_az_resources_project_commitments_trigger;
		DROP FUNCTION cluster_az_resources_project_commitments_trigger_function;
		DROP TABLE project_rates;
		DROP TABLE project_mail_notifications;
		DROP TABLE project_commitments;
		DROP TABLE project_az_resources;
		DROP TABLE project_resources;
		DROP INDEX project_services_stale_idx;
		DROP TABLE project_services;
		DROP TABLE projects;
		DROP TABLE domains;
		DROP TABLE cluster_rates;
		DROP TABLE cluster_az_resources;
		DROP TABLE cluster_resources;
		DROP TABLE cluster_services;
	`,
}
