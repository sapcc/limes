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
	"062_rename_cluster_level.down.sql": `
		ALTER TABLE services RENAME TO cluster_services;
		ALTER TABLE resources RENAME TO cluster_resources;
		ALTER TABLE az_resources RENAME TO cluster_az_resources;
		ALTER TABLE rates RENAME TO cluster_rates;

		ALTER SEQUENCE services_id_seq RENAME TO cluster_services_id_seq;
		ALTER SEQUENCE resources_id_seq RENAME TO cluster_resources_id_seq;
		ALTER SEQUENCE az_resources_id_seq RENAME TO cluster_az_resources_id_seq;
		ALTER SEQUENCE rates_id_seq RENAME TO cluster_rates_id_seq;
		
		ALTER TRIGGER services_check_path_values_trigger ON cluster_services RENAME TO cluster_services_check_path_values_trigger;
		ALTER TRIGGER resources_check_path_values_trigger ON cluster_resources RENAME TO cluster_resources_check_path_values_trigger;
		ALTER TRIGGER az_resources_check_path_values_trigger ON cluster_az_resources RENAME TO cluster_az_resources_check_path_values_trigger;
		ALTER TRIGGER az_resources_project_commitments_trigger ON cluster_az_resources RENAME TO cluster_az_resources_project_commitments_trigger;
	 	
		ALTER INDEX services_pkey RENAME TO cluster_services_pkey;
		ALTER INDEX services_id_liquid_version_key RENAME TO cluster_services_id_liquid_version_key;
	  	ALTER INDEX services_type_key RENAME TO cluster_services_type_key;
		ALTER INDEX resources_pkey RENAME TO cluster_resources_pkey;
		ALTER INDEX resources_path_key RENAME TO cluster_resources_path_key;
		ALTER INDEX resources_service_id_name_key RENAME TO cluster_resources_service_id_name_key;
	  	ALTER INDEX az_resources_pkey RENAME TO cluster_az_resources_pkey;
	  	ALTER INDEX az_resources_path_key RENAME TO cluster_az_resources_path_key;
	  	ALTER INDEX az_resources_resource_id_az_key RENAME TO cluster_az_resources_resource_id_az_key;
		ALTER INDEX rates_pkey RENAME TO cluster_rates_pkey;
	  	ALTER INDEX rates_service_id_name_key RENAME TO cluster_rates_service_id_name_key;
	  	      
		ALTER TABLE cluster_resources RENAME CONSTRAINT resources_service_id_fkey TO cluster_resources_service_id_fkey;
		ALTER TABLE cluster_resources RENAME CONSTRAINT resources_service_id_liquid_version_fkey TO cluster_resources_service_id_liquid_version_fkey;
		ALTER TABLE cluster_az_resources RENAME CONSTRAINT az_resources_resource_id_fkey TO cluster_az_resources_resource_id_fkey;
		ALTER TABLE cluster_rates RENAME CONSTRAINT rates_service_id_fkey TO cluster_rates_service_id_fkey;
		ALTER TABLE cluster_rates RENAME CONSTRAINT rates_service_id_liquid_version_fkey TO cluster_rates_service_id_liquid_version_fkey;

		CREATE OR REPLACE FUNCTION check_path_values_trigger_function()
			RETURNS trigger AS $$
			DECLARE
				problem RECORD;
			BEGIN
				FOR problem IN
					SELECT cr.id AS id, cr.path AS actual, CONCAT(cs.type, '/', cr.name) AS expected
					FROM cluster_resources cr JOIN cluster_services cs ON cr.service_id = cs.id
					WHERE cr.path != CONCAT(cs.type, '/', cr.name)
				LOOP
					RAISE EXCEPTION 'inconsistent value for cluster_resources.path: expected "%", but got "%" for ID %', problem.expected, problem.actual, problem.id;
				END LOOP;
		
				FOR problem IN
					SELECT cazr.id AS id, cazr.path AS actual, CONCAT(cr.path, '/', cazr.az) AS expected
					FROM cluster_az_resources cazr JOIN cluster_resources cr ON cazr.resource_id = cr.id
					WHERE cazr.path != CONCAT(cr.path, '/', cazr.az)
				LOOP
					RAISE EXCEPTION 'inconsistent value for cluster_az_resources.path: expected "%", but got "%" for ID %', problem.expected, problem.actual, problem.id;
				END LOOP;
		
				RETURN NEW;
			END;
			$$ LANGUAGE plpgsql;
	`,
	"062_rename_cluster_level.up.sql": `
		ALTER TABLE cluster_services RENAME TO services;
		ALTER TABLE cluster_resources RENAME TO resources;
		ALTER TABLE cluster_az_resources RENAME TO az_resources;
		ALTER TABLE cluster_rates RENAME TO rates;
		
		ALTER SEQUENCE cluster_services_id_seq RENAME TO services_id_seq;
		ALTER SEQUENCE cluster_resources_id_seq RENAME TO resources_id_seq;
		ALTER SEQUENCE cluster_az_resources_id_seq RENAME TO az_resources_id_seq;
		ALTER SEQUENCE cluster_rates_id_seq RENAME TO rates_id_seq;
		
		ALTER TRIGGER cluster_services_check_path_values_trigger ON services RENAME TO services_check_path_values_trigger;
		ALTER TRIGGER cluster_resources_check_path_values_trigger ON resources RENAME TO resources_check_path_values_trigger;
		ALTER TRIGGER cluster_az_resources_check_path_values_trigger ON az_resources RENAME TO az_resources_check_path_values_trigger;
		ALTER TRIGGER cluster_az_resources_project_commitments_trigger ON az_resources RENAME TO az_resources_project_commitments_trigger;
	 	
		ALTER INDEX cluster_services_pkey RENAME TO services_pkey;
		ALTER INDEX cluster_services_id_liquid_version_key RENAME TO services_id_liquid_version_key;
	  	ALTER INDEX cluster_services_type_key RENAME TO services_type_key;
		ALTER INDEX cluster_resources_pkey RENAME TO resources_pkey;
		ALTER INDEX cluster_resources_path_key RENAME TO resources_path_key;
		ALTER INDEX cluster_resources_service_id_name_key RENAME TO resources_service_id_name_key;
	  	ALTER INDEX cluster_az_resources_pkey RENAME TO az_resources_pkey;
	  	ALTER INDEX cluster_az_resources_path_key RENAME TO az_resources_path_key;
	  	ALTER INDEX cluster_az_resources_resource_id_az_key RENAME TO az_resources_resource_id_az_key;
		ALTER INDEX cluster_rates_pkey RENAME TO rates_pkey;
	  	ALTER INDEX cluster_rates_service_id_name_key RENAME TO rates_service_id_name_key;
	  	      
		ALTER TABLE resources RENAME CONSTRAINT cluster_resources_service_id_fkey TO resources_service_id_fkey;
		ALTER TABLE resources RENAME CONSTRAINT cluster_resources_service_id_liquid_version_fkey TO resources_service_id_liquid_version_fkey;
		ALTER TABLE az_resources RENAME CONSTRAINT cluster_az_resources_resource_id_fkey TO az_resources_resource_id_fkey;
		ALTER TABLE rates RENAME CONSTRAINT cluster_rates_service_id_fkey TO rates_service_id_fkey;
		ALTER TABLE rates RENAME CONSTRAINT cluster_rates_service_id_liquid_version_fkey TO rates_service_id_liquid_version_fkey;

		CREATE OR REPLACE FUNCTION check_path_values_trigger_function()
			RETURNS trigger AS $$
			DECLARE
				problem RECORD;
			BEGIN
				FOR problem IN
					SELECT cr.id AS id, cr.path AS actual, CONCAT(cs.type, '/', cr.name) AS expected
					FROM resources cr JOIN services cs ON cr.service_id = cs.id
					WHERE cr.path != CONCAT(cs.type, '/', cr.name)
				LOOP
					RAISE EXCEPTION 'inconsistent value for resources.path: expected "%", but got "%" for ID %', problem.expected, problem.actual, problem.id;
				END LOOP;
		
				FOR problem IN
					SELECT cazr.id AS id, cazr.path AS actual, CONCAT(cr.path, '/', cazr.az) AS expected
					FROM az_resources cazr JOIN resources cr ON cazr.resource_id = cr.id
					WHERE cazr.path != CONCAT(cr.path, '/', cazr.az)
				LOOP
					RAISE EXCEPTION 'inconsistent value for az_resources.path: expected "%", but got "%" for ID %', problem.expected, problem.actual, problem.id;
				END LOOP;
		
				RETURN NEW;
			END;
			$$ LANGUAGE plpgsql;
	`,
	`063_localize_cluster_level_timestamps.up.sql`: `
		ALTER TABLE services ALTER COLUMN scraped_at TYPE TIMESTAMPTZ USING scraped_at AT TIME ZONE 'UTC';
		ALTER TABLE services ALTER COLUMN next_scrape_at TYPE TIMESTAMPTZ USING next_scrape_at AT TIME ZONE 'UTC';
		ALTER TABLE project_mail_notifications ALTER COLUMN next_submission_at TYPE TIMESTAMPTZ USING next_submission_at AT TIME ZONE 'UTC';
	`,
	`063_localize_cluster_level_timestamps.down.sql`: `
		ALTER TABLE services ALTER COLUMN scraped_at TYPE TIMESTAMP USING scraped_at AT TIME ZONE 'UTC';
		ALTER TABLE services ALTER COLUMN next_scrape_at TYPE TIMESTAMP USING next_scrape_at AT TIME ZONE 'UTC';
		ALTER TABLE project_mail_notifications ALTER COLUMN next_submission_at TYPE TIMESTAMP USING next_submission_at AT TIME ZONE 'UTC';
	`,
}
