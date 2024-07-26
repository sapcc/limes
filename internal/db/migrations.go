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
	//NOTE: Migrations 1 through 36 have been rolled up into one at 2023-02-26
	// to better represent the current baseline of the DB schema.
	"036_rollup.down.sql": `
		DROP TABLE cluster_capacitors;
		DROP TABLE cluster_services;
		DROP TABLE cluster_resources;
		DROP TABLE cluster_az_resources;
		DROP TABLE domains;
		DROP TABLE domain_services;
		DROP TABLE domain_resources;
		DROP TABLE projects;
		DROP INDEX project_services_stale_idx;
		DROP TABLE project_services;
		DROP TABLE project_resources;
		DROP TABLE project_az_resources;
		DROP TABLE project_commitments;
		DROP TABLE project_rates;
	`,
	"036_rollup.up.sql": `
		---------- cluster level

		CREATE TABLE cluster_capacitors (
			capacitor_id          TEXT       NOT NULL,
			scraped_at            TIMESTAMP  DEFAULT NULL,
			scrape_duration_secs  REAL       NOT NULL DEFAULT 0,
			serialized_metrics    TEXT       NOT NULL DEFAULT '',
			next_scrape_at        TIMESTAMP  NOT NULL DEFAULT NOW(),
			scrape_error_message  TEXT       NOT NULL DEFAULT '',
			PRIMARY KEY (capacitor_id)
		);

		CREATE TABLE cluster_services (
			id          BIGSERIAL  NOT NULL PRIMARY KEY,
			type        TEXT       NOT NULL UNIQUE
		);

		CREATE TABLE cluster_resources (
			id               BIGSERIAL  NOT NULL PRIMARY KEY,
			capacitor_id     TEXT       NOT NULL REFERENCES cluster_capacitors ON DELETE CASCADE,
			service_id       BIGINT     NOT NULL REFERENCES cluster_services ON DELETE CASCADE,
			name             TEXT       NOT NULL,
			UNIQUE (service_id, name)
		);

		CREATE TABLE cluster_az_resources (
			id             BIGSERIAL  NOT NULL PRIMARY KEY,
			resource_id    BIGINT     NOT NULL REFERENCES cluster_resources ON DELETE CASCADE,
			az             TEXT       NOT NULL,
			raw_capacity   BIGINT     NOT NULL,
			usage          BIGINT     DEFAULT NULL,
			subcapacities  TEXT       NOT NULL DEFAULT '',
			UNIQUE (resource_id, az)
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
			id          BIGSERIAL  NOT NULL PRIMARY KEY,
			service_id  BIGINT     NOT NULL REFERENCES domain_services ON DELETE CASCADE,
			name        TEXT       NOT NULL,
			quota       BIGINT     NOT NULL,
			UNIQUE (service_id, name)
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
			next_scrape_at              TIMESTAMP  NOT NULL DEFAULT NOW(),
			rates_next_scrape_at        TIMESTAMP  NOT NULL DEFAULT NOW(),
			UNIQUE (project_id, type)
		);
		CREATE INDEX project_services_stale_idx ON project_services (stale);

		CREATE TABLE project_resources (
			id                     BIGSERIAL  NOT NULL PRIMARY KEY,
			service_id             BIGINT     NOT NULL REFERENCES project_services ON DELETE CASCADE,
			name                   TEXT       NOT NULL,
			quota                  BIGINT     DEFAULT NULL, -- null if resInfo.NoQuota == true
			backend_quota          BIGINT     DEFAULT NULL,
			desired_backend_quota  BIGINT     DEFAULT NULL,
			UNIQUE (service_id, name)
		);

		CREATE TABLE project_az_resources (
			id                BIGSERIAL  NOT NULL PRIMARY KEY,
			resource_id       BIGINT     NOT NULL REFERENCES project_resources ON DELETE CASCADE,
			az                TEXT       NOT NULL,
			quota             BIGINT     DEFAULT NULL, -- null if resInfo.NoQuota == true
			usage             BIGINT     NOT NULL,
			physical_usage    BIGINT     DEFAULT NULL,
			subresources      TEXT       NOT NULL DEFAULT '',
			historical_usage  TEXT       NOT NULL DEFAULT '',
			UNIQUE (resource_id, az)
		);

		CREATE TABLE project_commitments (
			id                 BIGSERIAL  NOT NULL PRIMARY KEY,
			az_resource_id     BIGINT     NOT NULL REFERENCES project_az_resources ON DELETE RESTRICT,
			amount             BIGINT     NOT NULL,
			duration           TEXT       NOT NULL,
			created_at         TIMESTAMP  NOT NULL,
			creator_uuid       TEXT       NOT NULL,
			creator_name       TEXT       NOT NULL,
			confirm_by         TIMESTAMP  DEFAULT NULL,
			confirmed_at       TIMESTAMP  DEFAULT NULL,
			expires_at         TIMESTAMP  NOT NULL,
			superseded_at      TIMESTAMP  DEFAULT NULL,
			predecessor_id     BIGINT     DEFAULT NULL REFERENCES project_commitments ON DELETE RESTRICT,
			transfer_status    TEXT       NOT NULL DEFAULT '',
			transfer_token     TEXT       NOT NULL DEFAULT '',
			state              TEXT       NOT NULL
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
	"037_service_specific_quota_constraints.down.sql": `
		ALTER TABLE project_resources
			DROP COLUMN min_quota,
			DROP COLUMN max_quota;
	`,
	"037_service_specific_quota_constraints.up.sql": `
		ALTER TABLE project_resources
			ADD COLUMN min_quota BIGINT DEFAULT NULL,
			ADD COLUMN max_quota BIGINT DEFAULT NULL;
	`,
	"038_multi_source_quota_constraints.down.sql": `
		ALTER TABLE project_resources
			RENAME COLUMN min_quota_from_backend TO min_quota;
		ALTER TABLE project_resources
			RENAME COLUMN max_quota_from_backend TO max_quota;
		ALTER TABLE project_resources
			DROP COLUMN max_quota_from_manual_override,
			DROP COLUMN override_quota_from_config;
	`,
	"038_multi_source_quota_constraints.up.sql": `
		ALTER TABLE project_resources
			RENAME COLUMN min_quota TO min_quota_from_backend;
		ALTER TABLE project_resources
			RENAME COLUMN max_quota TO max_quota_from_backend;
		ALTER TABLE project_resources
			ADD COLUMN max_quota_from_admin BIGINT DEFAULT NULL,
			ADD COLUMN override_quota_from_config BIGINT DEFAULT NULL;
	`,
	"039_remove_bursting.down.sql": `
		ALTER TABLE projects
			ADD COLUMN has_bursting BOOLEAN NOT NULL DEFAULT TRUE;
	`,
	"039_remove_bursting.up.sql": `
		ALTER TABLE projects
			DROP COLUMN has_bursting;
	`,
	"040_remove_domain_quota.down.sql": `
		CREATE TABLE domain_services (
			id         BIGSERIAL  NOT NULL PRIMARY KEY,
			domain_id  BIGINT     NOT NULL REFERENCES domains ON DELETE CASCADE,
			type       TEXT       NOT NULL,
			UNIQUE (domain_id, type)
		);

		CREATE TABLE domain_resources (
			id          BIGSERIAL  NOT NULL PRIMARY KEY,
			service_id  BIGINT     NOT NULL REFERENCES domain_services ON DELETE CASCADE,
			name        TEXT       NOT NULL,
			quota       BIGINT     NOT NULL,
			UNIQUE (service_id, name)
		);
	`,
	"040_remove_domain_quota.up.sql": `
		DROP TABLE domain_resources;
		DROP TABLE domain_services;
	`,
	"041_remove_project_resources_desired_backend_quota.down.sql": `
		ALTER TABLE project_resources
			ADD COLUMN desired_backend_quota BIGINT DEFAULT NULL;
		UPDATE project_resources SET desired_backend_quota = quota;
	`,
	"041_remove_project_resources_desired_backend_quota.up.sql": `
		ALTER TABLE project_resources
			DROP COLUMN desired_backend_quota;
	`,
	"042_add_project_services_quota_desynced_at.down.sql": `
		ALTER TABLE project_services
			DROP COLUMN quota_desynced_at;
	`,
	"042_add_project_services_quota_desynced_at.up.sql": `
		ALTER TABLE project_services
			ADD COLUMN quota_desynced_at TIMESTAMP DEFAULT NULL;
	`,
	"043_add_project_services_quota_sync_duration_secs.down.sql": `
		ALTER TABLE project_services
			DROP COLUMN quota_sync_duration_secs;
	`,
	"043_add_project_services_quota_sync_duration_secs.up.sql": `
		ALTER TABLE project_services
			ADD COLUMN quota_sync_duration_secs REAL NOT NULL DEFAULT 0;
	`,
	"044_add_unique_key_to_transfer_token.down.sql": `
	  	ALTER TABLE project_commitments DROP CONSTRAINT transfer_token_idx;
		UPDATE project_commitments SET transfer_token = '' where transfer_token is NULL;
		ALTER TABLE project_commitments 
			ALTER COLUMN transfer_token SET NOT NULL,
			ALTER COLUMN transfer_token SET DEFAULT '';
	`,
	"044_add_unique_key_to_transfer_token.up.sql": `
		ALTER TABLE project_commitments 
			ALTER COLUMN transfer_token DROP NOT NULL,
			ALTER COLUMN transfer_token SET DEFAULT NULL;
		UPDATE project_commitments SET transfer_token = NULL where transfer_token = '';
		ALTER TABLE project_commitments ADD CONSTRAINT transfer_token_idx UNIQUE (transfer_token);
		`,
}
