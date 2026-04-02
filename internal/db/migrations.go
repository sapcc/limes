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
		DROP TRIGGER cluster_az_resources_check_path_values_trigger ON cluster_az_resources;
		DROP TRIGGER cluster_resources_check_path_values_trigger ON cluster_resources;
		DROP TRIGGER cluster_services_check_path_values_trigger ON cluster_services;
		DROP FUNCTION check_path_values_trigger_function;
		DROP TRIGGER cluster_az_resources_project_commitments_trigger ON cluster_az_resources;
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
					SELECT r.id AS id, r.path AS actual, CONCAT(s.type, '/', r.name) AS expected
					FROM resources r JOIN services s ON r.service_id = s.id
					WHERE r.path != CONCAT(s.type, '/', r.name)
				LOOP
					RAISE EXCEPTION 'inconsistent value for resources.path: expected "%", but got "%" for ID %', problem.expected, problem.actual, problem.id;
				END LOOP;

				FOR problem IN
					SELECT azr.id AS id, azr.path AS actual, CONCAT(r.path, '/', azr.az) AS expected
					FROM az_resources azr JOIN resources r ON azr.resource_id = r.id
					WHERE azr.path != CONCAT(r.path, '/', azr.az)
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
	"064_handles_commitments.down.sql": `
		ALTER TABLE resources DROP COLUMN handles_commitments;
	`,
	"064_handles_commitments.up.sql": `
		ALTER TABLE resources ADD COLUMN handles_commitments BOOLEAN NOT NULL DEFAULT FALSE;
	`,

	// NOTE: While making a necessary modification to a trigger function, this also fixes that 062 did not rename that function.
	"065_use_liquid_commitment_status.down.sql": `
		UPDATE project_commitments
			SET status = 'active' WHERE status = 'confirmed';
		ALTER TABLE project_commitments
			RENAME status TO state;
		CREATE FUNCTION cluster_az_resources_project_commitments_trigger_function()
			RETURNS trigger AS $$
			BEGIN
				DELETE FROM project_commitments
					WHERE az_resource_id = OLD.id
					AND state IN ('expired', 'superseded');
				RETURN OLD;
			END;
			$$ LANGUAGE plpgsql;
		DROP TRIGGER az_resources_project_commitments_trigger ON az_resources;
		CREATE TRIGGER az_resources_project_commitments_trigger
			BEFORE DELETE ON az_resources
			FOR EACH ROW
			EXECUTE FUNCTION cluster_az_resources_project_commitments_trigger_function();
		DROP FUNCTION az_resources_project_commitments_trigger_function;
	`,
	"065_use_liquid_commitment_status.up.sql": `
		UPDATE project_commitments
			SET state = 'confirmed' WHERE state = 'active';
		ALTER TABLE project_commitments
			RENAME state TO status;
		CREATE FUNCTION az_resources_project_commitments_trigger_function()
			RETURNS trigger AS $$
			BEGIN
				DELETE FROM project_commitments
					WHERE az_resource_id = OLD.id
					AND status IN ('expired', 'superseded');
				RETURN OLD;
			END;
			$$ LANGUAGE plpgsql;
		DROP TRIGGER az_resources_project_commitments_trigger ON az_resources;
		CREATE TRIGGER az_resources_project_commitments_trigger
			BEFORE DELETE ON az_resources
			FOR EACH ROW
			EXECUTE FUNCTION az_resources_project_commitments_trigger_function();
		DROP FUNCTION cluster_az_resources_project_commitments_trigger_function;
	`,
	"066_forbid_autogrowth.down.sql": `
		ALTER TABLE project_resources DROP COLUMN forbid_autogrowth;
	`,
	"066_forbid_autogrowth.up.sql": `
		ALTER TABLE project_resources ADD COLUMN forbid_autogrowth BOOLEAN NOT NULL DEFAULT FALSE;
	`,
	"067_maxQuotaFromLocalAdmin.up.sql": `
		ALTER TABLE project_resources DROP COLUMN max_quota_from_local_admin;
	`,
	"067_maxQuotaFromLocalAdmin.down.sql": `
		ALTER TABLE project_resources ADD COLUMN max_quota_from_local_admin BIGINT;
	`,
	"068_introduce_transfer_started_at.down.sql": `
		ALTER TABLE project_commitments DROP COLUMN transfer_started_at;
	`,
	"068_introduce_transfer_started_at.up.sql": ExpandEnumPlaceholders(`
		ALTER TABLE project_commitments ADD COLUMN transfer_started_at TIMESTAMPTZ DEFAULT NULL;
		UPDATE project_commitments SET transfer_started_at = NOW() WHERE transfer_status = {{limesresources.CommitmentTransferStatusPublic}} AND transfer_started_at IS NULL;
	`),
	"069_transfer_status_check_constraint.down.sql": `
		ALTER TABLE project_commitments DROP CONSTRAINT transfer_status_check;
	`,
	"069_transfer_status_check_constraint.up.sql": ExpandEnumPlaceholders(`
		UPDATE project_commitments
			SET transfer_status = {{limesresources.CommitmentTransferStatusNone}}, transfer_token = NULL, transfer_started_at = NULL
			WHERE status IN ({{liquid.CommitmentStatusSuperseded}}, {{liquid.CommitmentStatusExpired}});
		ALTER TABLE project_commitments
			ADD CONSTRAINT transfer_status_check CHECK (status NOT IN ({{liquid.CommitmentStatusSuperseded}}, {{liquid.CommitmentStatusExpired}}) OR transfer_status = {{limesresources.CommitmentTransferStatusNone}});
	`),
	"070_remove_project_level_values.down.sql": ExpandEnumPlaceholders(`
		ALTER TABLE project_resources
			ADD COLUMN quota                         BIGINT     DEFAULT NULL, -- null if resInfo.NoQuota == true
			ADD COLUMN backend_quota                 BIGINT     DEFAULT NULL;
		UPDATE project_resources pr
			SET pr.quota = pazr.quota, pr.backend_quota = pazr.backend_quota
			FROM az_resources azr
			JOIN project_az_resources pazr ON pazr.az_resource_id = azr.id AND pazr.project_id = pr.project_id
			WHERE azr.resource_id = pr.resource_id AND azr.az = {{liquid.AvailabilityZoneTotal}};
		DELETE FROM project_az_resources WHERE az_resource_id IN (SELECT id FROM az_resources WHERE az = {{liquid.AvailabilityZoneTotal}});
		DELETE FROM az_resources WHERE az = {{liquid.AvailabilityZoneTotal}};
	`),
	"070_remove_project_level_values.up.sql": ExpandEnumPlaceholders(`
		-- We do a migration of the used values here, so that the APIs don't produce garbage after migration
		-- We skip fields subcapacities, subresources, historical usage, last_nonzero_raw_capacity as they only make sense when az-attributed or are unused.
		INSERT INTO az_resources (resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity, path)
			SELECT azr.resource_id, {{liquid.AvailabilityZoneTotal}} as az, SUM(raw_capacity), SUM(usage), '' AS subcapacities, NULL AS last_nonzero_raw_capacity, REPLACE(azr.path, azr.az, {{liquid.AvailabilityZoneTotal}}) AS path
			FROM az_resources azr
			GROUP BY azr.resource_id, REPLACE(azr.path, azr.az, {{liquid.AvailabilityZoneTotal}});
		INSERT INTO project_az_resources (project_id, az_resource_id, quota, usage, physical_usage, subresources, historical_usage, backend_quota)
			SELECT pazr.project_id, azr_total.id AS az_resource_id, pr.quota, SUM(pazr.usage), SUM(pazr.physical_usage), '' AS subresources, '' as historical_usage, pr.backend_quota
			FROM az_resources azr
			JOIN az_resources azr_total ON azr.resource_id = azr_total.resource_id AND azr_total.az = {{liquid.AvailabilityZoneTotal}}
			JOIN project_az_resources pazr ON pazr.az_resource_id = azr.id
			JOIN project_resources pr ON pr.project_id = pazr.project_id AND pr.resource_id = azr.resource_id
			GROUP BY pazr.project_id, azr_total.id, pr.quota, pr.backend_quota;
		ALTER TABLE project_resources
			DROP COLUMN quota,
			DROP COLUMN backend_quota;
	`),
	"071_add_commitment_deleted_at.down.sql": ExpandEnumPlaceholders(`
		ALTER TABLE project_commitments DROP COLUMN deleted_at;
		ALTER TABLE project_commitments
			DROP CONSTRAINT transfer_status_check;
		ALTER TABLE project_commitments
			ADD CONSTRAINT transfer_status_check CHECK (status NOT IN ({{liquid.CommitmentStatusSuperseded}}, {{liquid.CommitmentStatusExpired}}) OR transfer_status = {{limesresources.CommitmentTransferStatusNone}});
		CREATE OR REPLACE FUNCTION az_resources_project_commitments_trigger_function()
			RETURNS trigger AS $$
			BEGIN
				DELETE FROM project_commitments
					WHERE az_resource_id = OLD.id
					AND status IN ('expired', 'superseded');
				RETURN OLD;
			END;
			$$ LANGUAGE plpgsql;
	`),
	"071_add_commitment_deleted_at.up.sql": ExpandEnumPlaceholders(`
		ALTER TABLE project_commitments ADD COLUMN deleted_at TIMESTAMPTZ DEFAULT NULL;
		ALTER TABLE project_commitments
			DROP CONSTRAINT transfer_status_check;
		ALTER TABLE project_commitments
			ADD CONSTRAINT transfer_status_check CHECK (status NOT IN ({{liquid.CommitmentStatusSuperseded}}, {{liquid.CommitmentStatusExpired}}, {{util.CommitmentStatusDeleted}}) OR transfer_status = {{limesresources.CommitmentTransferStatusNone}});
		CREATE OR REPLACE FUNCTION az_resources_project_commitments_trigger_function()
			RETURNS trigger AS $$
			BEGIN
				DELETE FROM project_commitments
					WHERE az_resource_id = OLD.id
					AND status IN ('expired', 'superseded', 'deleted');
				RETURN OLD;
			END;
			$$ LANGUAGE plpgsql;
	`),
	`072_add_category_display_name.down.sql`: `
		ALTER TABLE services
			DROP COLUMN display_name;
		ALTER TABLE resources
			DROP COLUMN display_name,	
			DROP COLUMN category_id;
		DROP TABLE categories;
	`,
	`072_add_category_display_name.up.sql`: `
		CREATE TABLE categories (
			id BIGSERIAL NOT NULL PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			display_name TEXT NOT NULL
		);
		ALTER TABLE services 
		    ADD COLUMN display_name TEXT NOT NULL DEFAULT '';
		ALTER TABLE resources
		    ADD COLUMN display_name TEXT NOT NULL DEFAULT '',
		    -- fallback to 'default' is handled in application layer on read
		    ADD COLUMN category_id BIGINT DEFAULT NULL 
		    	REFERENCES categories ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;
	`,
	`073_add_rate_path.down.sql`: `
		DROP TRIGGER rates_check_path_values_trigger ON rates;
		CREATE OR REPLACE FUNCTION check_path_values_trigger_function()
			RETURNS trigger AS $$
			DECLARE
				problem RECORD;
			BEGIN
				FOR problem IN
					SELECT r.id AS id, r.path AS actual, CONCAT(s.type, '/', r.name) AS expected
					FROM resources r JOIN services s ON r.service_id = s.id
					WHERE r.path != CONCAT(s.type, '/', r.name)
				LOOP
					RAISE EXCEPTION 'inconsistent value for resources.path: expected "%", but got "%" for ID %', problem.expected, problem.actual, problem.id;
				END LOOP;

				FOR problem IN
					SELECT azr.id AS id, azr.path AS actual, CONCAT(r.path, '/', azr.az) AS expected
					FROM az_resources azr JOIN resources r ON azr.resource_id = r.id
					WHERE azr.path != CONCAT(r.path, '/', azr.az)
				LOOP
					RAISE EXCEPTION 'inconsistent value for az_resources.path: expected "%", but got "%" for ID %', problem.expected, problem.actual, problem.id;
				END LOOP;

				RETURN NEW;
			END;
			$$ LANGUAGE plpgsql;
		ALTER TABLE rates DROP COLUMN path;
	`,
	`073_add_rate_path.up.sql`: `
		ALTER TABLE rates ADD COLUMN path TEXT NOT NULL DEFAULT '';
		UPDATE rates SET path = CONCAT(s.type, '/', rates.name) FROM services s WHERE rates.service_id = s.id;
		ALTER TABLE rates ALTER COLUMN path DROP DEFAULT;
		ALTER TABLE rates ADD CONSTRAINT rates_path_key UNIQUE (path);
		CREATE OR REPLACE FUNCTION check_path_values_trigger_function()
			RETURNS trigger AS $$
			DECLARE
				problem RECORD;
			BEGIN
				FOR problem IN
					SELECT r.id AS id, r.path AS actual, CONCAT(s.type, '/', r.name) AS expected
					FROM resources r JOIN services s ON r.service_id = s.id
					WHERE r.path != CONCAT(s.type, '/', r.name)
				LOOP
					RAISE EXCEPTION 'inconsistent value for resources.path: expected "%", but got "%" for ID %', problem.expected, problem.actual, problem.id;
				END LOOP;

				FOR problem IN
					SELECT azr.id AS id, azr.path AS actual, CONCAT(r.path, '/', azr.az) AS expected
					FROM az_resources azr JOIN resources r ON azr.resource_id = r.id
					WHERE azr.path != CONCAT(r.path, '/', azr.az)
				LOOP
					RAISE EXCEPTION 'inconsistent value for az_resources.path: expected "%", but got "%" for ID %', problem.expected, problem.actual, problem.id;
				END LOOP;

				FOR problem IN
					SELECT ra.id AS id, ra.path AS actual, CONCAT(s.type, '/', ra.name) AS expected
					FROM rates ra JOIN services s ON ra.service_id = s.id
					WHERE ra.path != CONCAT(s.type, '/', ra.name)
				LOOP
					RAISE EXCEPTION 'inconsistent value for rates.path: expected "%", but got "%" for ID %', problem.expected, problem.actual, problem.id;
				END LOOP;

				RETURN NEW;
			END;
			$$ LANGUAGE plpgsql;
		CREATE CONSTRAINT TRIGGER rates_check_path_values_trigger
			AFTER INSERT OR UPDATE ON rates
			DEFERRABLE INITIALLY DEFERRED
			FOR EACH ROW
			EXECUTE FUNCTION check_path_values_trigger_function();
	`,
	`074_add_rate_display_name.down.sql`: `
		ALTER TABLE rates
			DROP COLUMN display_name;
	`,
	`074_073_add_rate_display_name.up.sql`: `
		ALTER TABLE rates
		    ADD COLUMN display_name TEXT NOT NULL DEFAULT '';
	`,

	// The following block was auto-generated from `pg_dump --schema-only` of the database as of 074 like this:
	// 1) in the shell: awk '/CREATE TABLE/{table=$3}/CONSTRAINT.*NOT NULL/{$1=table"."$1;print}' < pg_dump_output.sql
	// 2) then in vim: s/^public\.\(\w*\)\.\(\w*\) .* CONSTRAINT \(\w*\) NOT NULL,\?$/\t\tALTER TABLE \1 RENAME CONSTRAINT \3 TO \1_\2_not_null;/
	// 3) verified that `pg_dump --schema-only` now does not show any NOT NULL constraints with non-default names (the shell output from step 1 is empty now)
	"075_normalize_nonnull_constraint_names.up.sql": `
		ALTER TABLE az_resources RENAME CONSTRAINT cluster_az_resources_id_not_null TO az_resources_id_not_null;
		ALTER TABLE az_resources RENAME CONSTRAINT cluster_az_resources_resource_id_not_null TO az_resources_resource_id_not_null;
		ALTER TABLE az_resources RENAME CONSTRAINT cluster_az_resources_az_not_null TO az_resources_az_not_null;
		ALTER TABLE az_resources RENAME CONSTRAINT cluster_az_resources_raw_capacity_not_null TO az_resources_raw_capacity_not_null;
		ALTER TABLE az_resources RENAME CONSTRAINT cluster_az_resources_subcapacities_not_null TO az_resources_subcapacities_not_null;
		ALTER TABLE az_resources RENAME CONSTRAINT cluster_az_resources_path_not_null TO az_resources_path_not_null;
		ALTER TABLE project_commitments RENAME CONSTRAINT project_commitments_state_not_null TO project_commitments_status_not_null;
		ALTER TABLE rates RENAME CONSTRAINT cluster_rates_id_not_null TO rates_id_not_null;
		ALTER TABLE rates RENAME CONSTRAINT cluster_rates_service_id_not_null TO rates_service_id_not_null;
		ALTER TABLE rates RENAME CONSTRAINT cluster_rates_name_not_null TO rates_name_not_null;
		ALTER TABLE rates RENAME CONSTRAINT cluster_rates_liquid_version_not_null TO rates_liquid_version_not_null;
		ALTER TABLE rates RENAME CONSTRAINT cluster_rates_unit_not_null TO rates_unit_not_null;
		ALTER TABLE rates RENAME CONSTRAINT cluster_rates_topology_not_null TO rates_topology_not_null;
		ALTER TABLE rates RENAME CONSTRAINT cluster_rates_has_usage_not_null TO rates_has_usage_not_null;
		ALTER TABLE resources RENAME CONSTRAINT cluster_resources_id_not_null TO resources_id_not_null;
		ALTER TABLE resources RENAME CONSTRAINT cluster_resources_service_id_not_null TO resources_service_id_not_null;
		ALTER TABLE resources RENAME CONSTRAINT cluster_resources_name_not_null TO resources_name_not_null;
		ALTER TABLE resources RENAME CONSTRAINT cluster_resources_liquid_version_not_null TO resources_liquid_version_not_null;
		ALTER TABLE resources RENAME CONSTRAINT cluster_resources_unit_not_null TO resources_unit_not_null;
		ALTER TABLE resources RENAME CONSTRAINT cluster_resources_topology_not_null TO resources_topology_not_null;
		ALTER TABLE resources RENAME CONSTRAINT cluster_resources_has_capacity_not_null TO resources_has_capacity_not_null;
		ALTER TABLE resources RENAME CONSTRAINT cluster_resources_needs_resource_demand_not_null TO resources_needs_resource_demand_not_null;
		ALTER TABLE resources RENAME CONSTRAINT cluster_resources_has_quota_not_null TO resources_has_quota_not_null;
		ALTER TABLE resources RENAME CONSTRAINT cluster_resources_attributes_json_not_null TO resources_attributes_json_not_null;
		ALTER TABLE resources RENAME CONSTRAINT cluster_resources_path_not_null TO resources_path_not_null;
		ALTER TABLE services RENAME CONSTRAINT cluster_services_id_not_null TO services_id_not_null;
		ALTER TABLE services RENAME CONSTRAINT cluster_services_type_not_null TO services_type_not_null;
		ALTER TABLE services RENAME CONSTRAINT cluster_services_scrape_duration_secs_not_null TO services_scrape_duration_secs_not_null;
		ALTER TABLE services RENAME CONSTRAINT cluster_services_serialized_metrics_not_null TO services_serialized_metrics_not_null;
		ALTER TABLE services RENAME CONSTRAINT cluster_services_next_scrape_at_not_null TO services_next_scrape_at_not_null;
		ALTER TABLE services RENAME CONSTRAINT cluster_services_scrape_error_message_not_null TO services_scrape_error_message_not_null;
		ALTER TABLE services RENAME CONSTRAINT cluster_services_liquid_version_not_null TO services_liquid_version_not_null;
		ALTER TABLE services RENAME CONSTRAINT cluster_services_capacity_metric_families_json_not_null TO services_capacity_metric_families_json_not_null;
		ALTER TABLE services RENAME CONSTRAINT cluster_services_usage_metric_families_json_not_null TO services_usage_metric_families_json_not_null;
		ALTER TABLE services RENAME CONSTRAINT cluster_services_usage_report_needs_project_metadata_not_null TO services_usage_report_needs_project_metadata_not_null;
		ALTER TABLE services RENAME CONSTRAINT cluster_services_quota_update_needs_project_metadata_not_null TO services_quota_update_needs_project_metadata_not_null;
	`,
	"075_normalize_nonnull_constraint_names.down.sql": `
		ALTER TABLE az_resources RENAME CONSTRAINT az_resources_id_not_null TO cluster_az_resources_id_not_null;
		ALTER TABLE az_resources RENAME CONSTRAINT az_resources_resource_id_not_null TO cluster_az_resources_resource_id_not_null;
		ALTER TABLE az_resources RENAME CONSTRAINT az_resources_az_not_null TO cluster_az_resources_az_not_null;
		ALTER TABLE az_resources RENAME CONSTRAINT az_resources_raw_capacity_not_null TO cluster_az_resources_raw_capacity_not_null;
		ALTER TABLE az_resources RENAME CONSTRAINT az_resources_subcapacities_not_null TO cluster_az_resources_subcapacities_not_null;
		ALTER TABLE az_resources RENAME CONSTRAINT az_resources_path_not_null TO cluster_az_resources_path_not_null;
		ALTER TABLE project_commitments RENAME CONSTRAINT project_commitments_status_not_null TO project_commitments_state_not_null;
		ALTER TABLE rates RENAME CONSTRAINT rates_id_not_null TO cluster_rates_id_not_null;
		ALTER TABLE rates RENAME CONSTRAINT rates_service_id_not_null TO cluster_rates_service_id_not_null;
		ALTER TABLE rates RENAME CONSTRAINT rates_name_not_null TO cluster_rates_name_not_null;
		ALTER TABLE rates RENAME CONSTRAINT rates_liquid_version_not_null TO cluster_rates_liquid_version_not_null;
		ALTER TABLE rates RENAME CONSTRAINT rates_unit_not_null TO cluster_rates_unit_not_null;
		ALTER TABLE rates RENAME CONSTRAINT rates_topology_not_null TO cluster_rates_topology_not_null;
		ALTER TABLE rates RENAME CONSTRAINT rates_has_usage_not_null TO cluster_rates_has_usage_not_null;
		ALTER TABLE resources RENAME CONSTRAINT resources_id_not_null TO cluster_resources_id_not_null;
		ALTER TABLE resources RENAME CONSTRAINT resources_service_id_not_null TO cluster_resources_service_id_not_null;
		ALTER TABLE resources RENAME CONSTRAINT resources_name_not_null TO cluster_resources_name_not_null;
		ALTER TABLE resources RENAME CONSTRAINT resources_liquid_version_not_null TO cluster_resources_liquid_version_not_null;
		ALTER TABLE resources RENAME CONSTRAINT resources_unit_not_null TO cluster_resources_unit_not_null;
		ALTER TABLE resources RENAME CONSTRAINT resources_topology_not_null TO cluster_resources_topology_not_null;
		ALTER TABLE resources RENAME CONSTRAINT resources_has_capacity_not_null TO cluster_resources_has_capacity_not_null;
		ALTER TABLE resources RENAME CONSTRAINT resources_needs_resource_demand_not_null TO cluster_resources_needs_resource_demand_not_null;
		ALTER TABLE resources RENAME CONSTRAINT resources_has_quota_not_null TO cluster_resources_has_quota_not_null;
		ALTER TABLE resources RENAME CONSTRAINT resources_attributes_json_not_null TO cluster_resources_attributes_json_not_null;
		ALTER TABLE resources RENAME CONSTRAINT resources_path_not_null TO cluster_resources_path_not_null;
		ALTER TABLE services RENAME CONSTRAINT services_id_not_null TO cluster_services_id_not_null;
		ALTER TABLE services RENAME CONSTRAINT services_type_not_null TO cluster_services_type_not_null;
		ALTER TABLE services RENAME CONSTRAINT services_scrape_duration_secs_not_null TO cluster_services_scrape_duration_secs_not_null;
		ALTER TABLE services RENAME CONSTRAINT services_serialized_metrics_not_null TO cluster_services_serialized_metrics_not_null;
		ALTER TABLE services RENAME CONSTRAINT services_next_scrape_at_not_null TO cluster_services_next_scrape_at_not_null;
		ALTER TABLE services RENAME CONSTRAINT services_scrape_error_message_not_null TO cluster_services_scrape_error_message_not_null;
		ALTER TABLE services RENAME CONSTRAINT services_liquid_version_not_null TO cluster_services_liquid_version_not_null;
		ALTER TABLE services RENAME CONSTRAINT services_capacity_metric_families_json_not_null TO cluster_services_capacity_metric_families_json_not_null;
		ALTER TABLE services RENAME CONSTRAINT services_usage_metric_families_json_not_null TO cluster_services_usage_metric_families_json_not_null;
		ALTER TABLE services RENAME CONSTRAINT services_usage_report_needs_project_metadata_not_null TO cluster_services_usage_report_needs_project_metadata_not_null;
		ALTER TABLE services RENAME CONSTRAINT services_quota_update_needs_project_metadata_not_null TO cluster_services_quota_update_needs_project_metadata_not_null;
	`,
}
