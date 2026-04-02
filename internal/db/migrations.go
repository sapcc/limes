// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package db

import _ "embed"

//go:embed baseline.sql
var sqlBaseline string

var sqlMigrations = map[string]string{
	// NOTE: Migrations 1 through 75 have been rolled up into one at 2026-04-02
	// to better represent the current baseline of the DB schema.
	"075_rollup.up.sql": ExpandEnumPlaceholders(sqlBaseline),
	"075_rollup.down.sql": `
		DROP TRIGGER rates_check_path_values_trigger ON az_resources;
		DROP TRIGGER az_resources_check_path_values_trigger ON az_resources;
		DROP TRIGGER resources_check_path_values_trigger ON resources;
		DROP TRIGGER services_check_path_values_trigger ON services;
		DROP FUNCTION check_path_values_trigger_function;
		DROP TRIGGER az_resources_project_commitments_trigger ON az_resources;
		DROP FUNCTION az_resources_project_commitments_trigger_function;
		DROP TABLE project_rates;
		DROP TABLE project_mail_notifications;
		DROP TABLE project_commitments;
		DROP TABLE project_az_resources;
		DROP TABLE project_resources;
		DROP INDEX project_services_stale_idx;
		DROP TABLE project_services;
		DROP TABLE projects;
		DROP TABLE domains;
		DROP TABLE rates;
		DROP TABLE az_resources;
		DROP TABLE resources;
		DROP TABLE categories;
		DROP TABLE services;
	`,
}
