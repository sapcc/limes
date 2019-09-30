/*******************************************************************************
*
* Copyright 2017-2018 SAP SE
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

//SQLMigrations must be public because it's also used by pkg/test.
var SQLMigrations = map[string]string{
	"001_initial.down.sql": `
		DROP TABLE cluster_services;
		DROP TABLE cluster_resources;
		DROP TABLE domains;
		DROP TABLE domain_services;
		DROP TABLE domain_resources;
		DROP TABLE projects;
		DROP INDEX project_services_stale_idx;
		DROP TABLE project_services;
		DROP TABLE project_resources;
	`,
	"001_initial.up.sql": `
		---------- cluster level

		CREATE TABLE cluster_services (
		  id         BIGSERIAL NOT NULL PRIMARY KEY,
		  cluster_id TEXT      NOT NULL,
		  type       TEXT      NOT NULL,
		  scraped_at TIMESTAMP NOT NULL,
		  UNIQUE (cluster_id, type)
		);

		CREATE TABLE cluster_resources (
		  service_id BIGINT NOT NULL REFERENCES cluster_services ON DELETE CASCADE,
		  name       TEXT   NOT NULL,
		  capacity   BIGINT NOT NULL,
		  PRIMARY KEY (service_id, name)
		);

		---------- domain level

		CREATE TABLE domains (
		  id         BIGSERIAL NOT NULL PRIMARY KEY,
		  cluster_id TEXT      NOT NULL,
		  name       TEXT      NOT NULL,
		  uuid       TEXT      NOT NULL UNIQUE
		);

		CREATE TABLE domain_services (
		  id         BIGSERIAL NOT NULL PRIMARY KEY,
		  domain_id  BIGINT    NOT NULL REFERENCES domains ON DELETE CASCADE,
		  type       TEXT      NOT NULL,
		  UNIQUE (domain_id, type)
		);

		CREATE TABLE domain_resources (
		  service_id BIGINT NOT NULL REFERENCES domain_services ON DELETE CASCADE,
		  name       TEXT   NOT NULL,
		  quota      BIGINT NOT NULL,
		  PRIMARY KEY (service_id, name)
		);

		---------- project level

		CREATE TABLE projects (
		  id        BIGSERIAL NOT NULL PRIMARY KEY,
		  domain_id BIGINT    NOT NULL REFERENCES domains ON DELETE CASCADE,
		  name      TEXT      NOT NULL,
		  uuid      TEXT      NOT NULL UNIQUE
		);

		CREATE TABLE project_services (
		  id          BIGSERIAL NOT NULL PRIMARY KEY,
		  project_id  BIGINT    NOT NULL REFERENCES projects ON DELETE CASCADE,
		  type        TEXT      NOT NULL,
		  scraped_at  TIMESTAMP, -- defaults to NULL to indicate that scraping did not happen yet
		  stale       BOOLEAN   NOT NULL DEFAULT FALSE,
		  UNIQUE (project_id, type)
		);
		CREATE INDEX project_services_stale_idx ON project_services (stale);

		CREATE TABLE project_resources (
		  service_id    BIGINT NOT NULL REFERENCES project_services ON DELETE CASCADE,
		  name          TEXT   NOT NULL,
		  quota         BIGINT NOT NULL,
		  usage         BIGINT NOT NULL,
		  backend_quota BIGINT NOT NULL,
		  PRIMARY KEY (service_id, name)
		);
	`,
	"002_add_cluster_resource_comment.down.sql": `
		ALTER TABLE cluster_resources DROP COLUMN comment;
	`,
	"002_add_cluster_resource_comment.up.sql": `
		ALTER TABLE cluster_resources ADD COLUMN comment TEXT NOT NULL DEFAULT '';
	`,
	"003_add_project_parent_id.down.sql": `
		ALTER TABLE projects DROP COLUMN parent_uuid;
	`,
	"003_add_project_parent_id.up.sql": `
		ALTER TABLE projects ADD COLUMN parent_uuid TEXT NOT NULL DEFAULT '';
	`,
	"004_fix_domain_uuid_uniqueness.down.sql": `
		ALTER TABLE domains DROP CONSTRAINT domains_uuid_cluster_id_key;
		ALTER TABLE domains ADD UNIQUE (uuid);
	`,
	"004_fix_domain_uuid_uniqueness.up.sql": `
		ALTER TABLE domains DROP CONSTRAINT domains_uuid_key;
		ALTER TABLE domains ADD UNIQUE (uuid, cluster_id);
	`,
	"005_add_project_resource_subresources.down.sql": `
		ALTER TABLE project_resources DROP COLUMN subresources;
	`,
	"005_add_project_resource_subresources.up.sql": `
		ALTER TABLE project_resources ADD COLUMN subresources TEXT NOT NULL DEFAULT '';
	`,
	"006_add_cluster_resources_subcapacities.down.sql": `
		ALTER TABLE cluster_resources DROP COLUMN subcapacities;
	`,
	"006_add_cluster_resources_subcapacities.up.sql": `
		ALTER TABLE cluster_resources ADD COLUMN subcapacities TEXT NOT NULL DEFAULT '';
	`,
	"007_add_projects_has_bursting.down.sql": `
		ALTER TABLE projects DROP COLUMN has_bursting;
	`,
	"007_add_projects_has_bursting.up.sql": `
		ALTER TABLE projects ADD COLUMN has_bursting BOOLEAN NOT NULL DEFAULT TRUE;
	`,
	"008_add_project_resources_desired_backend_quota.down.sql": `
		ALTER TABLE project_resources DROP COLUMN desired_backend_quota;
	`,
	"008_add_project_resources_desired_backend_quota.up.sql": `
		ALTER TABLE project_resources ADD COLUMN desired_backend_quota BIGINT NOT NULL DEFAULT 0;
	`,
	"009_add_project_resources_physical_usage.down.sql": `
		ALTER TABLE project_resources DROP COLUMN physical_usage;
	`,
	"009_add_project_resources_physical_usage.up.sql": `
		ALTER TABLE project_resources ADD COLUMN physical_usage BIGINT DEFAULT NULL;
	`,
	"010_add_project_rate_limits.down.sql": `
		DROP TABLE project_rate_limits;
	`,
	"010_add_project_rate_limits.up.sql": `
		CREATE TABLE project_rate_limits (
			service_id      BIGINT NOT NULL REFERENCES project_services ON DELETE CASCADE,
			target_type_uri TEXT   NOT NULL,
			action          TEXT   NOT NULL,
			rate_limit      BIGINT NOT NULL,
			unit            TEXT   NOT NULL,
			PRIMARY KEY (service_id, target_type_uri, action)
		);
	`,
	"011_add_cluster_resources_capacity_per_az.down.sql": `
		ALTER TABLE cluster_resources DROP COLUMN capacity_per_az;
	`,
	"011_add_cluster_resources_capacity_per_az.up.sql": `
		ALTER TABLE cluster_resources ADD COLUMN capacity_per_az TEXT NOT NULL DEFAULT '';
	`,
}
