// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package db

import _ "embed"

//go:embed baseline.sql
var sqlBaseline string

var sqlMigrations = map[int64]string{
	// NOTE: Migrations 1 through 75 have been rolled up into one at 2026-04-02
	// to better represent the current baseline of the DB schema.
	75: ExpandEnumPlaceholders(sqlBaseline),
	76: `
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
	77: `
		ALTER TABLE rates ADD COLUMN category_id BIGINT DEFAULT NULL REFERENCES categories ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;
	`,
	78: `
		ALTER TABLE resources ADD CONSTRAINT resources_topology_acceptable_values CHECK (topology IN ('flat', 'az-aware', 'az-separated'));
		ALTER TABLE rates ADD CONSTRAINT rates_topology_acceptable_values CHECK (topology IN ('flat', 'az-aware', 'az-separated'));
	`,
	79: `
		ALTER TABLE services ADD COLUMN commitment_handling_needs_project_metadata BOOLEAN NOT NULL DEFAULT FALSE;
		ALTER TABLE rates ADD COLUMN from_liquid BOOLEAN NOT NULL DEFAULT FALSE;
	`,
	80: `
		ALTER TABLE rates DROP COLUMN from_liquid;
	`,
	81: `
		ALTER TABLE project_commitments ADD COLUMN updated_at TIMESTAMPTZ;
		UPDATE project_commitments SET updated_at = COALESCE(deleted_at, superseded_at, transfer_started_at, confirmed_at, created_at);
		ALTER TABLE project_commitments ALTER COLUMN updated_at SET NOT NULL;
	`,
	82: `
		UPDATE resources SET unit = 'piece' WHERE unit = '';
		UPDATE rates SET unit = 'piece' WHERE unit = '';
	`,

	// NOTE: This will fail if there are categories that belong to multiple services.
	//       (The `INSERT INTO category_affinities` part will complain about its uniqueness constraint being violated.)
	//       This is currently not the case in any of our real deployments, so this restriction ought to be acceptable;
	//       but it fails for some test databases, so an `rm -rf .testdb` may be required if this migration fails.
	//
	// NOTE: The table `category_affinities` is set up as a materialized temporary table,
	//       instead of just being a table expression within UPDATE,
	//       to ensure the explicit error on uniqueness violation as described above.
	83: `
		ALTER TABLE categories ADD COLUMN service_id BIGINT REFERENCES services ON DELETE CASCADE;
		ALTER TABLE categories DROP CONSTRAINT categories_name_key;
		ALTER TABLE categories ADD UNIQUE (service_id, name);

		CREATE TEMP TABLE category_affinities (category_id BIGINT UNIQUE, service_id BIGINT);
		INSERT INTO category_affinities
			SELECT DISTINCT category_id, service_id FROM (
				SELECT category_id, service_id FROM resources WHERE category_id IS NOT NULL
				UNION
				SELECT category_id, service_id FROM rates WHERE category_id IS NOT NULL
			);

		UPDATE categories c SET service_id = ca.service_id FROM category_affinities ca WHERE ca.category_id = c.id;
		DROP TABLE category_affinities;

		ALTER TABLE categories ALTER COLUMN service_id SET NOT NULL;
	`,
}
