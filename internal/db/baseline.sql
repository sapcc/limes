-- SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
-- SPDX-License-Identifier: Apache-2.0

---------- metadata

CREATE TABLE services (
	id                                   BIGSERIAL    NOT NULL PRIMARY KEY,
	type                                 TEXT         NOT NULL UNIQUE,
	scraped_at                           TIMESTAMPTZ  DEFAULT NULL,
	scrape_duration_secs                 REAL         NOT NULL DEFAULT 0,
	serialized_metrics                   TEXT         NOT NULL DEFAULT '',
	next_scrape_at                       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
	scrape_error_message                 TEXT         NOT NULL DEFAULT '',
	liquid_version                       BIGINT       NOT NULL DEFAULT 0,
	capacity_metric_families_json        TEXT         NOT NULL DEFAULT '',
	usage_metric_families_json           TEXT         NOT NULL DEFAULT '',
	usage_report_needs_project_metadata  BOOLEAN      NOT NULL DEFAULT FALSE,
	quota_update_needs_project_metadata  BOOLEAN      NOT NULL DEFAULT FALSE,
	display_name                         TEXT         NOT NULL DEFAULT '',
	UNIQUE (id, liquid_version)
);

CREATE TABLE categories (
	id            BIGSERIAL  NOT NULL PRIMARY KEY,
	name          TEXT       NOT NULL UNIQUE,
	display_name  TEXT       NOT NULL
);

CREATE TABLE resources (
	id                     BIGSERIAL  NOT NULL PRIMARY KEY,
	service_id             BIGINT     NOT NULL REFERENCES services ON DELETE CASCADE,
	name                   TEXT       NOT NULL,
	liquid_version         BIGINT     NOT NULL DEFAULT 0,
	unit                   TEXT       NOT NULL DEFAULT '',
	topology               TEXT       NOT NULL DEFAULT '',
	has_capacity           BOOLEAN    NOT NULL DEFAULT FALSE,
	needs_resource_demand  BOOLEAN    NOT NULL DEFAULT FALSE,
	has_quota              BOOLEAN    NOT NULL DEFAULT FALSE,
	attributes_json        TEXT       NOT NULL DEFAULT '',
	path                   TEXT       NOT NULL UNIQUE,
	handles_commitments    BOOLEAN    NOT NULL DEFAULT FALSE,
	display_name           TEXT       NOT NULL DEFAULT '',
	category_id            BIGINT     DEFAULT NULL REFERENCES categories ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
	UNIQUE (service_id, name),
	FOREIGN KEY (service_id, liquid_version) REFERENCES services (id, liquid_version) DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE az_resources (
	id                         BIGSERIAL  NOT NULL PRIMARY KEY,
	resource_id                BIGINT     NOT NULL REFERENCES resources ON DELETE CASCADE,
	az                         TEXT       NOT NULL,
	raw_capacity               BIGINT     NOT NULL,
	usage                      BIGINT     DEFAULT NULL,
	subcapacities              TEXT       NOT NULL DEFAULT '',
	last_nonzero_raw_capacity  BIGINT     DEFAULT NULL,
	path                       TEXT       NOT NULL UNIQUE,
	UNIQUE (resource_id, az)
);

CREATE TABLE rates (
	id              BIGSERIAL  NOT NULL PRIMARY KEY,
	service_id      BIGINT     NOT NULL REFERENCES services ON DELETE CASCADE,
	name            TEXT       NOT NULL,
	liquid_version  BIGINT     NOT NULL DEFAULT 0,
	unit            TEXT       NOT NULL DEFAULT '',
	topology        TEXT       NOT NULL DEFAULT '',
	has_usage       BOOLEAN    NOT NULL DEFAULT FALSE,
	path            TEXT       NOT NULL UNIQUE,
	display_name    TEXT       NOT NULL DEFAULT '',
	UNIQUE (service_id, name),
	FOREIGN KEY (service_id, liquid_version) REFERENCES services (id, liquid_version) DEFERRABLE INITIALLY DEFERRED
);

---------- project data

CREATE TABLE domains (
	id    BIGSERIAL  NOT NULL PRIMARY KEY,
	name  TEXT       NOT NULL,
	uuid  TEXT       NOT NULL UNIQUE
);

CREATE TABLE projects (
	id           BIGSERIAL  NOT NULL PRIMARY KEY,
	domain_id    BIGINT     NOT NULL REFERENCES domains ON DELETE CASCADE,
	name         TEXT       NOT NULL,
	uuid         TEXT       NOT NULL UNIQUE,
	parent_uuid  TEXT       NOT NULL DEFAULT ''
);

CREATE TABLE project_services (
	id                        BIGSERIAL    NOT NULL PRIMARY KEY,
	project_id                BIGINT       NOT NULL REFERENCES projects ON DELETE CASCADE,
	service_id                BIGINT       NOT NULL REFERENCES services ON DELETE CASCADE,
	scraped_at                TIMESTAMPTZ  DEFAULT NULL, -- null if scraping did not happen yet
	stale                     BOOLEAN      NOT NULL DEFAULT FALSE,
	scrape_duration_secs      REAL         NOT NULL DEFAULT 0,
	serialized_scrape_state   TEXT         NOT NULL DEFAULT '',
	serialized_metrics        TEXT         NOT NULL DEFAULT '',
	checked_at                TIMESTAMPTZ  DEFAULT NULL,
	scrape_error_message      TEXT         NOT NULL DEFAULT '',
	next_scrape_at            TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
	quota_desynced_at         TIMESTAMPTZ  DEFAULT NULL,
	quota_sync_duration_secs  REAL         NOT NULL DEFAULT 0,
	UNIQUE (project_id, service_id)
);
CREATE INDEX project_services_stale_idx ON project_services (stale, next_scrape_at);

CREATE TABLE project_resources (
	id                            BIGSERIAL  NOT NULL PRIMARY KEY,
	project_id                    BIGINT     NOT NULL REFERENCES projects ON DELETE CASCADE,
	resource_id                   BIGINT     NOT NULL REFERENCES resources ON DELETE CASCADE,
	max_quota_from_outside_admin  BIGINT     DEFAULT NULL,
	override_quota_from_config    BIGINT     DEFAULT NULL,
	forbidden                     BOOLEAN    NOT NULL DEFAULT FALSE,
	forbid_autogrowth             BOOLEAN    NOT NULL DEFAULT FALSE,
	UNIQUE (project_id, resource_id)
);

CREATE TABLE project_az_resources (
	id                BIGSERIAL  NOT NULL PRIMARY KEY,
	project_id        BIGINT     NOT NULL REFERENCES projects ON DELETE CASCADE,
	az_resource_id    BIGINT     NOT NULL REFERENCES az_resources ON DELETE CASCADE,
	quota             BIGINT     DEFAULT NULL, -- null if resources.has_quota == false
	usage             BIGINT     NOT NULL,
	physical_usage    BIGINT     DEFAULT NULL,
	subresources      TEXT       NOT NULL DEFAULT '',
	historical_usage  TEXT       NOT NULL DEFAULT '',
	backend_quota     BIGINT     DEFAULT NULL,
	UNIQUE (project_id, az_resource_id)
);

CREATE TABLE project_commitments (
	id                       BIGSERIAL    NOT NULL PRIMARY KEY,
	uuid                     TEXT         NOT NULL UNIQUE,
	project_id               BIGINT       NOT NULL REFERENCES projects ON DELETE RESTRICT,
	az_resource_id           BIGINT       NOT NULL REFERENCES az_resources ON DELETE RESTRICT, -- we circumvent this constraint for expired/superseded/deleted commitments by using a trigger
	status                   TEXT         NOT NULL,
	amount                   BIGINT       NOT NULL,
	duration                 TEXT         NOT NULL,
	created_at               TIMESTAMPTZ  NOT NULL,
	creator_uuid             TEXT         NOT NULL,
	creator_name             TEXT         NOT NULL,
	confirm_by               TIMESTAMPTZ  DEFAULT NULL,
	confirmed_at             TIMESTAMPTZ  DEFAULT NULL,
	expires_at               TIMESTAMPTZ  NOT NULL,
	superseded_at            TIMESTAMPTZ  DEFAULT NULL,
	transfer_status          TEXT         NOT NULL DEFAULT '',
	transfer_token           TEXT         DEFAULT NULL UNIQUE, -- default is NULL instead of '' to enable the uniqueness constraint below
	notify_on_confirm        BOOLEAN      NOT NULL DEFAULT FALSE,
	notified_for_expiration  BOOLEAN      NOT NULL DEFAULT FALSE,
	creation_context_json    JSONB        NOT NULL,
	supersede_context_json   JSONB        DEFAULT NULL,
	renew_context_json       JSONB        DEFAULT NULL,
	transfer_started_at      TIMESTAMPTZ  DEFAULT NULL,
	deleted_at               TIMESTAMPTZ  DEFAULT NULL,
	CONSTRAINT transfer_status_check CHECK (status NOT IN ({{liquid.CommitmentStatusSuperseded}}, {{liquid.CommitmentStatusExpired}}, {{util.CommitmentStatusDeleted}}) OR transfer_status = {{limesresources.CommitmentTransferStatusNone}})
);

CREATE TABLE project_mail_notifications (
	id                  BIGSERIAL    NOT NULL PRIMARY KEY,
	project_id          BIGINT       NOT NULL REFERENCES projects ON DELETE CASCADE,
	subject             TEXT         NOT NULL,
	body                TEXT         NOT NULL,
	next_submission_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
	failed_submissions  BIGINT       NOT NULL DEFAULT 0
);

CREATE TABLE project_rates (
	id               BIGSERIAL  NOT NULL PRIMARY KEY,
	project_id       BIGINT     NOT NULL REFERENCES projects ON DELETE CASCADE,
	rate_id          BIGINT     NOT NULL REFERENCES rates ON DELETE CASCADE,
	rate_limit       BIGINT     DEFAULT NULL, -- null = not rate-limited
	window_ns        BIGINT     DEFAULT NULL, -- null = not rate-limited, unit = nanoseconds
	usage_as_bigint  TEXT       NOT NULL,     -- empty = not scraped
	UNIQUE (project_id, rate_id)
);

---------- triggers

-- Records in `project_commitments` will usually block the deletion of the associated `az_resources` record.
-- Commitments in statuses "expired", "superseded" or "deleted" are an exception to this, because of this trigger.
CREATE FUNCTION az_resources_project_commitments_trigger_function()
	RETURNS trigger AS $$
	BEGIN
		DELETE FROM project_commitments
			WHERE az_resource_id = OLD.id
			AND status IN ({{liquid.CommitmentStatusExpired}}, {{liquid.CommitmentStatusSuperseded}}, {{util.CommitmentStatusDeleted}});
		RETURN OLD;
	END;
	$$ LANGUAGE plpgsql;

CREATE TRIGGER az_resources_project_commitments_trigger
	BEFORE DELETE ON az_resources
	FOR EACH ROW
	EXECUTE FUNCTION az_resources_project_commitments_trigger_function();

-- This function validates that all `resources.path` and `az_resources.path` are consistent.

CREATE FUNCTION check_path_values_trigger_function()
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

CREATE CONSTRAINT TRIGGER services_check_path_values_trigger
	AFTER INSERT OR UPDATE ON services
	DEFERRABLE INITIALLY DEFERRED
	FOR EACH ROW
	EXECUTE FUNCTION check_path_values_trigger_function();
CREATE CONSTRAINT TRIGGER resources_check_path_values_trigger
	AFTER INSERT OR UPDATE ON resources
	DEFERRABLE INITIALLY DEFERRED
	FOR EACH ROW
	EXECUTE FUNCTION check_path_values_trigger_function();
CREATE CONSTRAINT TRIGGER az_resources_check_path_values_trigger
	AFTER INSERT OR UPDATE ON az_resources
	DEFERRABLE INITIALLY DEFERRED
	FOR EACH ROW
	EXECUTE FUNCTION check_path_values_trigger_function();
CREATE CONSTRAINT TRIGGER rates_check_path_values_trigger
	AFTER INSERT OR UPDATE ON rates
	DEFERRABLE INITIALLY DEFERRED
	FOR EACH ROW
	EXECUTE FUNCTION check_path_values_trigger_function();
