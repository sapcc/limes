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
	`076_notify_service_update.up.sql`: `
		CREATE OR REPLACE FUNCTION notify_service_update()
			RETURNS trigger AS $$
			DECLARE
				service_type TEXT;
			BEGIN
				IF TG_TABLE_NAME = 'services' THEN
					FOR service_type IN
						SELECT s.type FROM services s
							WHERE s.type = ANY(ARRAY[NEW.type, OLD.type])
					LOOP
						PERFORM pg_notify('limitas_service_update', service_type);
					END LOOP;

				ELSIF TG_TABLE_NAME = 'resources' OR TG_TABLE_NAME = 'rates' THEN
					FOR service_type IN
						SELECT s.type FROM services s
							WHERE s.id = ANY(ARRAY[NEW.service_id, OLD.service_id])
					LOOP
						PERFORM pg_notify('limitas_service_update', service_type);
					END LOOP;

				ELSIF TG_TABLE_NAME = 'az_resources' THEN
					FOR service_type IN
						SELECT DISTINCT s.type FROM services s
							JOIN resources r ON r.service_id = s.id
							WHERE r.id = ANY(ARRAY[NEW.resource_id, OLD.resource_id])
					LOOP
						PERFORM pg_notify('limitas_service_update', service_type);
					END LOOP;
				END IF;

				RETURN COALESCE(NEW, OLD);
			END;
			$$ LANGUAGE plpgsql;

		CREATE TRIGGER services_notify_update
			AFTER INSERT OR UPDATE OR DELETE ON services
			FOR EACH ROW EXECUTE FUNCTION notify_service_update();

		CREATE TRIGGER resources_notify_update
			AFTER INSERT OR UPDATE OR DELETE ON resources
			FOR EACH ROW EXECUTE FUNCTION notify_service_update();

		CREATE TRIGGER az_resources_notify_update
			AFTER INSERT OR UPDATE OR DELETE ON az_resources
			FOR EACH ROW EXECUTE FUNCTION notify_service_update();

		CREATE TRIGGER rates_notify_update
			AFTER INSERT OR UPDATE OR DELETE ON rates
			FOR EACH ROW EXECUTE FUNCTION notify_service_update();
	`,
	`076_notify_service_update.down.sql`: `
		DROP TRIGGER IF EXISTS rates_notify_update ON rates;
		DROP TRIGGER IF EXISTS az_resources_notify_update ON az_resources;
		DROP TRIGGER IF EXISTS resources_notify_update ON resources;
		DROP TRIGGER IF EXISTS services_notify_update ON services;
		DROP FUNCTION IF EXISTS notify_service_update;
	`,
	`077_rate_category.up.sql`: `
		ALTER TABLE rates ADD COLUMN category_id BIGINT DEFAULT NULL REFERENCES categories ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;
	`,
	`077_rate_category.down.sql`: `
		ALTER TABLE rates DROP COLUMN category_id;
	`,
	`078_add_topology_check.up.sql`: `
		ALTER TABLE resources ADD CONSTRAINT resources_topology_acceptable_values CHECK (topology IN ('flat', 'az-aware', 'az-separated'));
		ALTER TABLE rates ADD CONSTRAINT rates_topology_acceptable_values CHECK (topology IN ('flat', 'az-aware', 'az-separated'));
	`,
	`078_add_topology_check.down.sql`: `
		ALTER TABLE resources DROP CONSTRAINT resources_topology_acceptable_values;
		ALTER TABLE rates DROP CONSTRAINT rates_topology_acceptable_values;
	`,
}
