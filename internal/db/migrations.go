/*******************************************************************************
*
* Copyright 2017-2020 SAP SE
*
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You should have received a copy of the License along with this
* program. If not, you may obtain a copy of the License at
*
*     http://www.apache.org/licenses/LICENSE-2.0
*
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
*
*******************************************************************************/

package db

var sqlMigrations = map[string]string{
	//NOTE: Migrations 1 through 21 have been rolled up into one at 2023-01-09
	//to better represent the current baseline of the DB schema.
	"021_rollup.down.sql": `
		DROP TABLE cluster_capacitors;
		DROP TABLE cluster_services;
		DROP TABLE cluster_resources;
		DROP TABLE domains;
		DROP TABLE domain_services;
		DROP TABLE domain_resources;
		DROP TABLE projects;
		DROP INDEX project_services_stale_idx;
		DROP TABLE project_services;
		DROP TABLE project_resources;
		DROP TABLE project_rates;
	`,
	"021_rollup.up.sql": `
		---------- cluster level

		CREATE TABLE cluster_capacitors (
			capacitor_id          TEXT       NOT NULL,
			scraped_at            TIMESTAMP  NOT NULL,
			scrape_duration_secs  REAL       NOT NULL DEFAULT 0,
			serialized_metrics    TEXT       NOT NULL DEFAULT '',
			PRIMARY KEY (capacitor_id)
		);

		CREATE TABLE cluster_services (
			id          BIGSERIAL  NOT NULL PRIMARY KEY,
			type        TEXT       NOT NULL UNIQUE,
			scraped_at  TIMESTAMP  NOT NULL
		);

		CREATE TABLE cluster_resources (
			service_id      BIGINT  NOT NULL REFERENCES cluster_services ON DELETE CASCADE,
			name            TEXT    NOT NULL,
			capacity        BIGINT  NOT NULL,
			subcapacities   TEXT    NOT NULL DEFAULT '',
			capacity_per_az TEXT    NOT NULL DEFAULT '',
			PRIMARY KEY (service_id, name)
		);

		---------- domain level

		CREATE TABLE domains (
			id    BIGSERIAL  NOT NULL PRIMARY KEY,
			name  TEXT       NOT NULL,
			uuid  TEXT       NOT NULL UNIQUE
		);

		CREATE TABLE domain_services (
			id         BIGSERIAL  NOT NULL PRIMARY KEY,
			domain_id  BIGINT     NOT NULL REFERENCES domains ON DELETE CASCADE,
			type       TEXT       NOT NULL,
			UNIQUE (domain_id, type)
		);

		CREATE TABLE domain_resources (
			service_id  BIGINT  NOT NULL REFERENCES domain_services ON DELETE CASCADE,
			name        TEXT    NOT NULL,
			quota       BIGINT  NOT NULL,
			PRIMARY KEY (service_id, name)
		);

		---------- project level

		CREATE TABLE projects (
			id            BIGSERIAL  NOT NULL PRIMARY KEY,
			domain_id     BIGINT     NOT NULL REFERENCES domains ON DELETE CASCADE,
			name          TEXT       NOT NULL,
			uuid          TEXT       NOT NULL UNIQUE,
			parent_uuid   TEXT       NOT NULL DEFAULT '',
			has_bursting  BOOLEAN    NOT NULL DEFAULT TRUE
		);

		CREATE TABLE project_services (
			id                          BIGSERIAL  NOT NULL PRIMARY KEY,
			project_id                  BIGINT     NOT NULL REFERENCES projects ON DELETE CASCADE,
			type                        TEXT       NOT NULL,
			scraped_at                  TIMESTAMP  DEFAULT NULL, -- null if scraping did not happen yet
			stale                       BOOLEAN    NOT NULL DEFAULT FALSE,
			scrape_duration_secs        REAL       NOT NULL DEFAULT 0,
			rates_scraped_at            TIMESTAMP  DEFAULT NULL, -- null if scraping did not happen yet
			rates_stale                 BOOLEAN    NOT NULL DEFAULT FALSE,
			rates_scrape_duration_secs  REAL       NOT NULL DEFAULT 0,
			rates_scrape_state          TEXT       NOT NULL DEFAULT '',
			serialized_metrics          TEXT       NOT NULL DEFAULT '',
			checked_at                  TIMESTAMP  DEFAULT NULL,
			scrape_error_message        TEXT       NOT NULL DEFAULT '',
			rates_checked_at            TIMESTAMP  DEFAULT NULL,
			rates_scrape_error_message  TEXT       NOT NULL DEFAULT '',
			UNIQUE (project_id, type)
		);
		CREATE INDEX project_services_stale_idx ON project_services (stale);

		CREATE TABLE project_resources (
			service_id             BIGINT  NOT NULL REFERENCES project_services ON DELETE CASCADE,
			name                   TEXT    NOT NULL,
			quota                  BIGINT  DEFAULT NULL, -- null if resInfo.NoQuota == true
			usage                  BIGINT  NOT NULL,
			backend_quota          BIGINT  DEFAULT NULL,
			subresources           TEXT    NOT NULL DEFAULT '',
			desired_backend_quota  BIGINT  DEFAULT NULL,
			physical_usage         BIGINT  DEFAULT NULL,
			PRIMARY KEY (service_id, name)
		);

		CREATE TABLE project_rates (
			service_id       BIGINT  NOT NULL REFERENCES project_services ON DELETE CASCADE,
			name             TEXT    NOT NULL,
			rate_limit       BIGINT  DEFAULT NULL, -- null = not rate-limited
			window_ns        BIGINT  DEFAULT NULL, -- null = not rate-limited, unit = nanoseconds
			usage_as_bigint  TEXT    NOT NULL,     -- empty = not scraped
			PRIMARY KEY (service_id, name)
		);
	`,
	"022_add_next_scrape_at.up.sql": `
		ALTER TABLE project_services
			ADD COLUMN next_scrape_at TIMESTAMP NOT NULL DEFAULT NOW(),
			ADD COLUMN rates_next_scrape_at TIMESTAMP NOT NULL DEFAULT NOW();
		UPDATE project_services SET
			next_scrape_at = COALESCE(checked_at, NOW()) + interval '30 minutes',
			rates_next_scrape_at = COALESCE(rates_checked_at, NOW()) + interval '30 minutes';
	`,
	"022_add_next_scrape_at.down.sql": `
		ALTER TABLE project_services
			DROP COLUMN next_scrape_at,
			DROP COLUMN rates_next_scrape_at;
	`,
	// NOTE: cluster_resources.capacitor_id must start out as DEFAULT NULL because existing rows have no values here
	"023_capacity_scan_rework.up.sql": `
		ALTER TABLE cluster_capacitors
			ADD COLUMN next_scrape_at TIMESTAMP NOT NULL DEFAULT NOW();
		ALTER TABLE cluster_resources
			ADD COLUMN capacitor_id TEXT DEFAULT NULL REFERENCES cluster_capacitors ON DELETE CASCADE;
	`,
	"023_capacity_scan_rework.down.sql": `
		ALTER TABLE cluster_capacitors
			DROP COLUMN next_scrape_at;
		ALTER TABLE cluster_resources
			DROP COLUMN capacitor_id;
	`,
	"024_move_capacity_scrape_timestamps.up.sql": `
		ALTER TABLE cluster_capacitors
			ALTER COLUMN scraped_at DROP NOT NULL; -- null if scraping did not happen yet
		ALTER TABLE cluster_services
			DROP COLUMN scraped_at;
		ALTER TABLE cluster_resources
			ALTER COLUMN capacitor_id DROP DEFAULT;
		ALTER TABLE cluster_resources
			ALTER COLUMN capacitor_id SET NOT NULL;
	`,
	"024_move_capacity_scrape_timestamps.down.sql": `
		ALTER TABLE cluster_capacitors
			ALTER COLUMN scraped_at DROP DEFAULT;
		ALTER TABLE cluster_services
			ADD COLUMN scraped_at TIMESTAMP NOT NULL DEFAULT NOW();
		ALTER TABLE cluster_resources
			ALTER COLUMN capacitor_id DROP NOT NULL;
		ALTER TABLE cluster_resources
			ALTER COLUMN capacitor_id SET DEFAULT NULL;
	`,
	"025_capacity_scan_rework.up.sql": `
		ALTER TABLE cluster_capacitors
			ADD COLUMN scrape_error_message TEXT NOT NULL DEFAULT '';
	`,
	"025_capacity_scan_rework.down.sql": `
		ALTER TABLE cluster_capacitors
			DROP COLUMN scrape_error_message;
	`,
	"026_commitments.up.sql": `
		CREATE TABLE project_commitments (
			id                 BIGSERIAL  NOT NULL PRIMARY KEY,
			service_id         BIGINT     NOT NULL REFERENCES project_services ON DELETE RESTRICT,
			resource_name      TEXT       NOT NULL,
			availability_zone  TEXT       NOT NULL,
			amount             BIGINT     NOT NULL,
			duration           TEXT       NOT NULL,
			requested_at       TIMESTAMP  NOT NULL,
			confirm_after      TIMESTAMP  NOT NULL,
			confirmed_at       TIMESTAMP  DEFAULT NULL,
			expires_at         TIMESTAMP  DEFAULT NULL,
			superseded_at      TIMESTAMP  DEFAULT NULL,
			predecessor_id     BIGINT     DEFAULT NULL REFERENCES project_commitments ON DELETE RESTRICT,
			transfer_status    TEXT       NOT NULL DEFAULT '',
			transfer_token     TEXT       NOT NULL DEFAULT ''
		);
	`,
	"026_commitments.down.sql": `
		DROP TABLE project_commitments;
	`,

	// NOTE: 027 could be done much easier by converting the old primary key into
	// a UNIQUE constraint and creating a new BIGSERIAL column with primary key.
	// However, this would cause `id` to be the last column, which I find very confusing.
	//
	// NOTE 2: The constraint renames are necessary to ensure that the schema will remain the same
	// after the next rollup, when the table swaps in this migration will not happen anymore in new setups.
	//
	// Since we have the chance here, this also moves the `cluster_resources.capacitor_id` column further to the front.
	"027_add_id_columns.up.sql": `
		ALTER TABLE cluster_resources RENAME TO cluster_resources_old;
		CREATE TABLE cluster_resources (
			id               BIGSERIAL  NOT NULL PRIMARY KEY,
			capacitor_id     TEXT       NOT NULL REFERENCES cluster_capacitors ON DELETE CASCADE,
			service_id       BIGINT     NOT NULL REFERENCES cluster_services ON DELETE CASCADE,
			name             TEXT       NOT NULL,
			capacity         BIGINT     NOT NULL,
			subcapacities    TEXT       NOT NULL DEFAULT '',
			capacity_per_az  TEXT       NOT NULL DEFAULT '',
			UNIQUE (service_id, name)
		);
		INSERT INTO cluster_resources (service_id, name, capacity, subcapacities, capacity_per_az, capacitor_id)
			SELECT service_id, name, capacity, subcapacities, capacity_per_az, capacitor_id FROM cluster_resources_old
			ORDER BY service_id, name;
		DROP TABLE cluster_resources_old;
		ALTER TABLE cluster_resources
			RENAME CONSTRAINT cluster_resources_pkey1 TO cluster_resources_pkey;
		ALTER TABLE cluster_resources
			RENAME CONSTRAINT cluster_resources_capacitor_id_fkey1 TO cluster_resources_capacitor_id_fkey;
		ALTER TABLE cluster_resources
			RENAME CONSTRAINT cluster_resources_service_id_fkey1 TO cluster_resources_service_id_fkey;

		ALTER TABLE domain_resources RENAME TO domain_resources_old;
		CREATE TABLE domain_resources (
			id          BIGSERIAL  NOT NULL PRIMARY KEY,
			service_id  BIGINT     NOT NULL REFERENCES domain_services ON DELETE CASCADE,
			name        TEXT       NOT NULL,
			quota       BIGINT     NOT NULL,
			UNIQUE (service_id, name)
		);
		INSERT INTO domain_resources (service_id, name, quota)
			SELECT service_id, name, quota FROM domain_resources_old
			ORDER BY service_id, name;
		DROP TABLE domain_resources_old;
		ALTER TABLE domain_resources
			RENAME CONSTRAINT domain_resources_pkey1 TO domain_resources_pkey;
		ALTER TABLE domain_resources
			RENAME CONSTRAINT domain_resources_service_id_fkey1 TO domain_resources_service_id_fkey;

		ALTER TABLE project_resources RENAME TO project_resources_old;
		CREATE TABLE project_resources (
			id                     BIGSERIAL  NOT NULL PRIMARY KEY,
			service_id             BIGINT     NOT NULL REFERENCES project_services ON DELETE CASCADE,
			name                   TEXT       NOT NULL,
			quota                  BIGINT     DEFAULT NULL, -- null if resInfo.NoQuota == true
			usage                  BIGINT     NOT NULL,
			backend_quota          BIGINT     DEFAULT NULL,
			subresources           TEXT       NOT NULL DEFAULT '',
			desired_backend_quota  BIGINT     DEFAULT NULL,
			physical_usage         BIGINT     DEFAULT NULL,
			UNIQUE (service_id, name)
		);
		INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage)
			SELECT service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage FROM project_resources_old
			ORDER BY service_id, name;
		DROP TABLE project_resources_old;
		ALTER TABLE project_resources
			RENAME CONSTRAINT project_resources_pkey1 TO project_resources_pkey;
		ALTER TABLE project_resources
			RENAME CONSTRAINT project_resources_service_id_fkey1 TO project_resources_service_id_fkey;
	`,
	"027_add_id_columns.down.sql": `
		ALTER TABLE cluster_resources RENAME TO cluster_resources_old;
		CREATE TABLE cluster_resources (
			service_id       BIGINT  NOT NULL REFERENCES cluster_services ON DELETE CASCADE,
			name             TEXT    NOT NULL,
			capacity         BIGINT  NOT NULL,
			subcapacities    TEXT    NOT NULL DEFAULT '',
			capacity_per_az  TEXT    NOT NULL DEFAULT '',
			capacitor_id     TEXT    NOT NULL REFERENCES cluster_capacitors ON DELETE CASCADE,
			PRIMARY KEY (service_id, name)
		);
		INSERT INTO cluster_resources (service_id, name, capacity, subcapacities, capacity_per_az, capacitor_id)
			SELECT service_id, name, capacity, subcapacities, capacity_per_az, capacitor_id FROM cluster_resources_old
			ORDER BY service_id, name;
		DROP TABLE cluster_resources_old;
		ALTER TABLE cluster_resources
			RENAME CONSTRAINT cluster_resources_pkey1 TO cluster_resources_pkey;
		ALTER TABLE cluster_resources
			RENAME CONSTRAINT cluster_resources_capacitor_id_fkey1 TO cluster_resources_capacitor_id_fkey;
		ALTER TABLE cluster_resources
			RENAME CONSTRAINT cluster_resources_service_id_fkey1 TO cluster_resources_service_id_fkey;

		ALTER TABLE domain_resources RENAME TO domain_resources_old;
		CREATE TABLE domain_resources (
			service_id  BIGINT  NOT NULL REFERENCES domain_services ON DELETE CASCADE,
			name        TEXT    NOT NULL,
			quota       BIGINT  NOT NULL,
			PRIMARY KEY (service_id, name)
		);
		INSERT INTO domain_resources (service_id, name, quota)
			SELECT service_id, name, quota FROM domain_resources_old
			ORDER BY service_id, name;
		DROP TABLE domain_resources_old;
		ALTER TABLE domain_resources
			RENAME CONSTRAINT domain_resources_pkey1 TO domain_resources_pkey;
		ALTER TABLE domain_resources
			RENAME CONSTRAINT domain_resources_service_id_fkey1 TO domain_resources_service_id_fkey;

		ALTER TABLE project_resources RENAME TO project_resources_old;
		CREATE TABLE project_resources (
			service_id             BIGINT  NOT NULL REFERENCES project_services ON DELETE CASCADE,
			name                   TEXT    NOT NULL,
			quota                  BIGINT  DEFAULT NULL, -- null if resInfo.NoQuota == true
			usage                  BIGINT  NOT NULL,
			backend_quota          BIGINT  DEFAULT NULL,
			subresources           TEXT    NOT NULL DEFAULT '',
			desired_backend_quota  BIGINT  DEFAULT NULL,
			physical_usage         BIGINT  DEFAULT NULL,
			PRIMARY KEY (service_id, name)
		);
		INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage)
			SELECT service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage FROM project_resources_old
			ORDER BY service_id, name;
		DROP TABLE project_resources_old;
		ALTER TABLE project_resources
			RENAME CONSTRAINT project_resources_pkey1 TO project_resources_pkey;
		ALTER TABLE project_resources
			RENAME CONSTRAINT project_resources_service_id_fkey1 TO project_resources_service_id_fkey;
	`,

	"028_add_cluster_az_resources.up.sql": `
		CREATE TABLE cluster_az_resources (
			resource_id    BIGINT  NOT NULL REFERENCES cluster_resources ON DELETE CASCADE,
			az             TEXT    NOT NULL,
			raw_capacity   BIGINT  NOT NULL,
			usage          BIGINT  NOT NULL,
			subcapacities  TEXT    NOT NULL DEFAULT '',
			UNIQUE (resource_id, az)
		);
	`,
	"028_add_cluster_az_resources.down.sql": `
		DROP TABLE cluster_az_resources;
	`,
	"029_add_project_az_resources.up.sql": `
		CREATE TABLE project_az_resources (
			resource_id     BIGINT  NOT NULL REFERENCES project_resources ON DELETE CASCADE,
			az              TEXT    NOT NULL,
			quota           BIGINT  DEFAULT NULL, -- null if resInfo.NoQuota == true
			usage           BIGINT  NOT NULL,
			physical_usage  BIGINT  DEFAULT NULL,
			subresources    TEXT    NOT NULL DEFAULT '',
			UNIQUE (resource_id, az)
		);
	`,
	"029_add_project_az_resources.down.sql": `
		DROP TABLE project_az_resources;
	`,
	"030_drop_redundant_resources_columns.up.sql": `
		ALTER TABLE cluster_resources
			DROP COLUMN capacity,
			DROP COLUMN subcapacities,
			DROP COLUMN capacity_per_az;
		ALTER TABLE project_resources
			DROP COLUMN usage,
			DROP COLUMN subresources,
			DROP COLUMN physical_usage;
	`,
	"030_drop_redundant_resources_columns.down.sql": `
		ALTER TABLE cluster_resources
			ADD COLUMN capacity BIGINT NOT NULL,
			ADD COLUMN subcapacities TEXT NOT NULL DEFAULT '',
			ADD COLUMN capacity_per_az TEXT NOT NULL DEFAULT '';
		ALTER TABLE project_resources
			ADD COLUMN usage BIGINT NOT NULL,
			ADD COLUMN subresources TEXT NOT NULL DEFAULT '',
			ADD COLUMN physical_usage BIGINT DEFAULT NULL;
	`,
	"031_fix_cluster_usage_typing.up.sql": `
		ALTER TABLE cluster_az_resources
			ALTER COLUMN usage DROP NOT NULL;
	`,
	"031_fix_cluster_usage_typing.down.sql": `
		ALTER TABLE cluster_az_resources
			ALTER COLUMN usage SET NOT NULL;
	`,
	"032_commitment_rework.up.sql": `
		ALTER TABLE project_commitments
			RENAME COLUMN confirm_after TO confirm_by;
		ALTER TABLE project_commitments
			ALTER COLUMN confirm_by DROP NOT NULL;
		ALTER TABLE project_commitments
			RENAME COLUMN requested_at TO created_at;
		ALTER TABLE project_commitments
			ADD COLUMN creator_uuid TEXT NOT NULL DEFAULT '',
			ADD COLUMN creator_name TEXT NOT NULL DEFAULT '';
		ALTER TABLE project_commitments
			ALTER COLUMN creator_uuid DROP DEFAULT,
			ALTER COLUMN creator_name DROP DEFAULT;
	`,
	"032_commitment_rework.down.sql": `
		ALTER TABLE project_commitments
			RENAME COLUMN confirm_by TO confirm_after;
		ALTER TABLE project_commitments
			ALTER COLUMN confirm_after SET NOT NULL;
		ALTER TABLE project_commitments
			RENAME COLUMN created_at TO requested_at;
		ALTER TABLE project_commitments
			DROP COLUMN creator_uuid,
			DROP COLUMN creator_name;
	`,
}
