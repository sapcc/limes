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
}
