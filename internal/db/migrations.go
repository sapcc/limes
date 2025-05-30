// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package db

var sqlMigrations = map[string]string{
	// NOTE: Migrations 1 through 44 have been rolled up into one at 2024-10-21
	// to better represent the current baseline of the DB schema.
	"044_rollup.down.sql": `
		DROP TABLE cluster_capacitors;
		DROP TABLE cluster_services;
		DROP TABLE cluster_resources;
		DROP TABLE cluster_az_resources;
		DROP TABLE domains;
		DROP TABLE projects;
		DROP INDEX project_services_stale_idx;
		DROP TABLE project_services;
		DROP TABLE project_resources;
		DROP TABLE project_az_resources;
		DROP TABLE project_commitments;
		DROP TABLE project_rates;
	`,
	"044_rollup.up.sql": `
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

		---------- project level

		CREATE TABLE projects (
			id            BIGSERIAL  NOT NULL PRIMARY KEY,
			domain_id     BIGINT     NOT NULL REFERENCES domains ON DELETE CASCADE,
			name          TEXT       NOT NULL,
			uuid          TEXT       NOT NULL UNIQUE,
			parent_uuid   TEXT       NOT NULL DEFAULT ''
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
			quota_desynced_at           TIMESTAMP  DEFAULT NULL,
			quota_sync_duration_secs    REAL       NOT NULL DEFAULT 0,
			UNIQUE (project_id, type)
		);
		CREATE INDEX project_services_stale_idx ON project_services (stale);

		CREATE TABLE project_resources (
			id                          BIGSERIAL  NOT NULL PRIMARY KEY,
			service_id                  BIGINT     NOT NULL REFERENCES project_services ON DELETE CASCADE,
			name                        TEXT       NOT NULL,
			quota                       BIGINT     DEFAULT NULL, -- null if resInfo.NoQuota == true
			backend_quota               BIGINT     DEFAULT NULL,
			min_quota_from_backend      BIGINT     DEFAULT NULL,
			max_quota_from_backend      BIGINT     DEFAULT NULL,
			max_quota_from_admin        BIGINT     DEFAULT NULL,
			override_quota_from_config  BIGINT     DEFAULT NULL,
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
			transfer_token     TEXT       DEFAULT NULL, -- default is NULL instead of '' to enable the uniqueness constraint below
			state              TEXT       NOT NULL,
			UNIQUE (transfer_token)
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
	"045_service_specific_quota_constraints.down.sql": `
		ALTER TABLE project_resources
			DROP max_quota_from_local_admin;
		ALTER TABLE project_resources
			RENAME COLUMN max_quota_from_outside_admin TO max_quota_from_admin;
	`,
	"045_service_specific_quota_constraints.up.sql": `
		ALTER TABLE project_resources
			ADD max_quota_from_local_admin BIGINT DEFAULT NULL;
		ALTER TABLE project_resources
			RENAME COLUMN max_quota_from_admin TO max_quota_from_outside_admin;
	`,
	"046_az_backend_quota.down.sql": `
		ALTER TABLE project_az_resources
			DROP COLUMN backend_quota;
	`,
	"046_az_backend_quota.up.sql": `
		ALTER TABLE project_az_resources
			ADD COLUMN backend_quota BIGINT default NULL;
	`,
	"047_confirmation_notification.down.sql": `
	ALTER TABLE project_commitments
		DROP COLUMN notify_on_confirm;
	`,
	"047_confirmation_notification.up.sql": `
		ALTER TABLE project_commitments
			ADD COLUMN notify_on_confirm BOOLEAN NOT NULL DEFAULT FALSE;
	`,
	"048_confirmation_notification.down.sql": `
	ALTER TABLE project_commitments
		DROP COLUMN notified_for_expiration;
	`,
	"048_confirmation_notification.up.sql": `
		ALTER TABLE project_commitments
			ADD COLUMN notified_for_expiration BOOLEAN NOT NULL DEFAULT FALSE;
	`,
	"049_project_mail_notifications.down.sql": `
		DROP TABLE project_mail_notifications;
	`,
	"049_project_mail_notifications.up.sql": `
		CREATE TABLE project_mail_notifications (
			id BIGSERIAL NOT NULL PRIMARY KEY,
			project_id BIGINT NOT NULL REFERENCES projects ON DELETE CASCADE,
			subject TEXT NOT NULL,
			body TEXT NOT NULL,
			next_submission_at TIMESTAMP NOT NULL DEFAULT NOW(),
			failed_submissions BIGINT NOT NULL DEFAULT 0
		);
	`,
	"050_commitment_workflow_context.down.sql": `
		-- We will probably not need this, no implementation for now
	`,
	"050_commitment_workflow_context.up.sql": `
		-- Step 1: Create new fields for commitment workflow contexts
		ALTER TABLE project_commitments
			ADD COLUMN creation_context_json JSONB,
			ADD COLUMN supersede_context_json JSONB;

		-- Step 2: Populate creation context
		WITH creation_context_data AS (
			SELECT pc.id as commitment_id, pc.predecessor_id,
				CASE
					WHEN pc.predecessor_id IS NULL THEN 'create'
					WHEN EXISTS (
						SELECT 1
						FROM project_commitments pc2
						-- Since the az_resource_id can change if a commitment is transferred to a different project,
						-- we need to join up to project_services and compare the service type and resource name
						JOIN project_az_resources pc2_az_res ON pc2.az_resource_id = pc2_az_res.id
						JOIN project_resources pc2_res ON pc2_az_res.resource_id = pc2_res.id
						JOIN project_services pc2_srv ON pc2_res.service_id = pc2_srv.id
						JOIN project_az_resources pc_az_res ON pc.az_resource_id = pc_az_res.id
						JOIN project_resources pc_res ON pc_az_res.resource_id = pc_res.id
						JOIN project_services pc_srv ON pc_res.service_id = pc_srv.id
						WHERE pc2.id = pc.predecessor_id
						AND pc2_res.name = pc_res.name
						AND pc2_srv.type = pc_srv.type
					) THEN 'split'
					ELSE 'convert'
				END AS creation_reason
			FROM project_commitments pc
		)
		UPDATE project_commitments
		SET creation_context_json = jsonb_build_object(
			'reason', creation_context_data.creation_reason,
			'related_ids',
			CASE
				WHEN creation_context_data.predecessor_id IS NULL THEN '[]'::jsonb
				ELSE jsonb_build_array(creation_context_data.predecessor_id)
			END
		)
		FROM creation_context_data
		WHERE project_commitments.id = creation_context_data.commitment_id;

		-- Step 3: Make creation context mandatory after populating with values
		ALTER TABLE project_commitments
			ALTER COLUMN creation_context_json SET NOT NULL;

		-- Step 4: Populate supersede context
		WITH supersede_context_data AS (
			SELECT pc.id AS superseded_id, pc2.id AS successor_id, pc2.az_resource_id AS successor_az_resource_id,
				CASE
					WHEN EXISTS (
						SELECT 1
						FROM project_az_resources pc2_az_res
						JOIN project_resources pc2_res ON pc2_az_res.resource_id = pc2_res.id
						JOIN project_services pc2_srv ON pc2_res.service_id = pc2_srv.id
						JOIN project_az_resources pc_az_res ON pc.az_resource_id = pc_az_res.id
						JOIN project_resources pc_res ON pc_az_res.resource_id = pc_res.id
						JOIN project_services pc_srv ON pc_res.service_id = pc_srv.id
						WHERE pc2_az_res.id = pc2.az_resource_id
						AND pc2_res.name = pc_res.name
						AND pc2_srv.type = pc_srv.type
					) THEN 'split'
					ELSE 'convert'
				END AS supersede_reason
			FROM project_commitments pc
			JOIN project_commitments pc2
				ON pc.id = pc2.predecessor_id
			WHERE pc.state = 'superseded'
		),
		-- When splitting or during conversion, it is possible that two or more successor commits are created
		aggregated_successors AS (
			SELECT superseded_id,
				jsonb_agg(successor_id) AS related_successors
			FROM supersede_context_data
			GROUP BY superseded_id
		)
		UPDATE project_commitments p1
		SET supersede_context_json = jsonb_build_object(
				'reason', scd.supersede_reason,
				'related_ids', aggregated_successors.related_successors
			)
		FROM supersede_context_data scd
		JOIN aggregated_successors
			ON scd.superseded_id = aggregated_successors.superseded_id
		WHERE p1.id = scd.superseded_id;

		-- Step 5: Remove deprecated field predecessor_id
		ALTER TABLE project_commitments
			DROP COLUMN predecessor_id;
	`,
	"051_commitment_renwal.down.sql": `
		ALTER TABLE project_commitments
			DROP COLUMN renew_context_json;
	`,
	"051_commitment_renewal.up.sql": `
		ALTER TABLE project_commitments
			ADD COLUMN renew_context_json JSONB;
	`,
	"052_capacitors_removal.down.sql": `
		ALTER TABLE cluster_services
			DROP COLUMN scraped_at,
			DROP COLUMN scrape_duration_secs,
			DROP COLUMN serialized_metrics,
			DROP COLUMN next_scrape_at,
			DROP COLUMN scrape_error_message;
		CREATE TABLE cluster_capacitors (
			capacitor_id text NOT NULL,
			scraped_at timestamp without time zone,
			scrape_duration_secs real DEFAULT 0 NOT NULL,
			serialized_metrics text DEFAULT ''::text NOT NULL,
			next_scrape_at timestamp without time zone DEFAULT now() NOT NULL,
			scrape_error_message text DEFAULT ''::text NOT NULL
		);
		ALTER TABLE cluster_resources
			ADD COLUMN capacitor_id TEXT NOT NULL;
  `,
	"052_capacitors_removal.up.sql": `
		ALTER TABLE cluster_resources
			DROP COLUMN capacitor_id;
		DROP TABLE cluster_capacitors;
		ALTER TABLE cluster_services
			ADD COLUMN scraped_at timestamp without time zone,
			ADD COLUMN scrape_duration_secs real DEFAULT 0 NOT NULL,
			ADD COLUMN serialized_metrics text DEFAULT ''::text NOT NULL,
			ADD COLUMN next_scrape_at timestamp without time zone DEFAULT now() NOT NULL,
			ADD COLUMN scrape_error_message text DEFAULT ''::text NOT NULL;
  `,
	"053_project_resources_forbidden.down.sql": `
		ALTER TABLE project_resources
			ADD COLUMN min_quota_from_backend BIGINT DEFAULT NULL,
			ADD COLUMN max_quota_from_backend BIGINT DEFAULT NULL;

		UPDATE project_resources
			SET max_quota_from_backend = 0
			WHERE forbidden = TRUE;

		ALTER TABLE project_resources
			DROP COLUMN forbidden;
	`,
	"053_project_resources_forbidden.up.sql": `
		ALTER TABLE project_resources
			ADD COLUMN forbidden BOOLEAN NOT NULL DEFAULT FALSE;

		UPDATE project_resources
			SET forbidden = TRUE
			WHERE max_quota_from_backend = 0;

		ALTER TABLE project_resources
			DROP COLUMN min_quota_from_backend,
			DROP COLUMN max_quota_from_backend;
	`,
	"054_persist_service_info.down.sql": `
		DROP TABLE cluster_rates;
		ALTER TABLE cluster_resources
			DROP COLUMN liquid_version,
			DROP COLUMN unit,
			DROP COLUMN topology,
			DROP COLUMN has_capacity,
			DROP COLUMN needs_resource_demand,
			DROP COLUMN has_quota,
			DROP COLUMN attributes_json;
		ALTER TABLE cluster_services
			DROP COLUMN liquid_version,
			DROP COLUMN capacity_metric_families_json,
			DROP COLUMN usage_metric_families_json,
			DROP COLUMN usage_report_needs_project_metadata,
			DROP COLUMN quota_update_needs_project_metadata;
	`,
	"054_persist_service_info.up.sql": `
		CREATE TABLE cluster_rates (
			id              BIGSERIAL  NOT NULL PRIMARY KEY,
			service_id      BIGINT     NOT NULL REFERENCES cluster_services ON DELETE CASCADE,
			name            TEXT       NOT NULL,
			liquid_version  BIGINT     NOT NULL DEFAULT 0,
			unit            TEXT       NOT NULL DEFAULT '',
			topology        TEXT       NOT NULL DEFAULT '',
			has_usage       BOOLEAN    NOT NULL DEFAULT FALSE,
			UNIQUE (service_id, name)
		);
		ALTER TABLE cluster_resources
			ADD COLUMN liquid_version         BIGINT   NOT NULL DEFAULT 0,
			ADD COLUMN unit                   TEXT     NOT NULL DEFAULT '',
			ADD COLUMN topology               TEXT     NOT NULL DEFAULT '',
			ADD COLUMN has_capacity           BOOLEAN  NOT NULL DEFAULT FALSE,
			ADD COLUMN needs_resource_demand  BOOLEAN  NOT NULL DEFAULT FALSE,
			ADD COLUMN has_quota              BOOLEAN  NOT NULL DEFAULT FALSE,
			ADD COLUMN attributes_json        TEXT     NOT NULL DEFAULT '';
		ALTER TABLE cluster_services
			ADD COLUMN liquid_version                         BIGINT   NOT NULL DEFAULT 0,
			ADD COLUMN capacity_metric_families_json          TEXT     NOT NULL DEFAULT '',
			ADD COLUMN usage_metric_families_json             TEXT     NOT NULL DEFAULT '',
			ADD COLUMN usage_report_needs_project_metadata    BOOLEAN  NOT NULL DEFAULT FALSE,
			ADD COLUMN quota_update_needs_project_metadata    BOOLEAN  NOT NULL DEFAULT FALSE;
	`,
	"055_add_cluster_az_resources_last_nonzero_raw_capacity.down.sql": `
		ALTER TABLE cluster_az_resources
			DROP COLUMN last_nonzero_raw_capacity;
	`,
	"055_add_cluster_az_resources_last_nonzero_raw_capacity.up.sql": `
		ALTER TABLE cluster_az_resources
			ADD COLUMN last_nonzero_raw_capacity BIGINT DEFAULT NULL;
	`,
	"056_merge_scrape_and_scrape_rates_job.down.sql": `
		ALTER TABLE project_services
			ADD COLUMN rates_scraped_at			   TIMESTAMP  DEFAULT NULL,	
			ADD COLUMN rates_stale                 BOOLEAN    NOT NULL DEFAULT FALSE,
			ADD COLUMN rates_scrape_duration_secs  REAL       NOT NULL DEFAULT 0,
			ADD COLUMN rates_checked_at            TIMESTAMP  DEFAULT NULL,
			ADD COLUMN rates_scrape_error_message  TEXT       NOT NULL DEFAULT '',
			ADD COLUMN rates_next_scrape_at        TIMESTAMP  NOT NULL DEFAULT NOW();
	`,
	"056_merge_scrape_and_scrape_rates_job.up.sql": `
		ALTER TABLE project_services
			DROP COLUMN rates_scraped_at,
			DROP COLUMN rates_stale,
			DROP COLUMN rates_scrape_duration_secs,
			DROP COLUMN rates_checked_at,
			DROP COLUMN rates_scrape_error_message,
			DROP COLUMN rates_next_scrape_at;
	`,
}
