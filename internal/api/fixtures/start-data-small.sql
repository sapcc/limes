-- This start-data contains exactly one project and one service, with all capacity/quota/usage values at 0.
-- It can be used as a base to set up isolated tests for individual reporting features.

CREATE OR REPLACE FUNCTION UNIXUTC(i integer) RETURNS timestamp AS $$ SELECT TO_TIMESTAMP(i) AT TIME ZONE 'Etc/UTC' $$ LANGUAGE SQL;
CREATE OR REPLACE FUNCTION UNIX(i integer) RETURNS timestamp AS $$ SELECT TO_TIMESTAMP(i) AT LOCAL $$ LANGUAGE SQL;

INSERT INTO cluster_services (id, type, scraped_at, next_scrape_at, liquid_version) VALUES (1, 'first', UNIXUTC(1000), UNIXUTC(2000), 1);

INSERT INTO cluster_resources (id, service_id, name, liquid_version, topology, has_quota) VALUES (1, 1, 'things', 1, 'flat', TRUE);
INSERT INTO cluster_resources (id, service_id, name, liquid_version, unit, topology, has_capacity, needs_resource_demand, has_quota) VALUES (2, 1, 'capacity', 1, 'B', 'az-aware', TRUE, TRUE, TRUE);

-- "capacity" is modeled as AZ-aware, "things" is not
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities) VALUES (1, 1, 'any', 0, 0, '');
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities) VALUES (2, 2, 'any', 0, 0, '');
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities) VALUES (3, 2, 'az-one', 0, 0, '');
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities) VALUES (4, 2, 'az-two', 0, 0, '');
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities) VALUES (5, 2, 'unknown', 0, 0, '');

INSERT INTO domains (id, name, uuid) VALUES (1, 'domainone', 'uuid-for-domainone');

INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'projectone', 'uuid-for-projectone', 'uuid-for-domainone');

INSERT INTO project_services_v2 (id, project_id, service_id, scraped_at, checked_at) VALUES (1, 1, 1, UNIX(11), UNIX(11));

INSERT INTO project_resources_v2 (id, project_id, resource_id, quota, backend_quota) VALUES (1,  1, 1,   0, 0);
INSERT INTO project_resources_v2 (id, project_id, resource_id, quota, backend_quota) VALUES (2,  1, 2, 0, 0);

-- "capacity" is modeled as AZ-aware, "things" is not
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, quota, usage, physical_usage, subresources) VALUES (1,  1,  1, 0,    0, NULL, '');
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, quota, usage, physical_usage, subresources) VALUES (2,  1,  2, 0,    0, NULL, '');
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, quota, usage, physical_usage, subresources) VALUES (3,  1,  3, 0,    0, NULL, '');
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, quota, usage, physical_usage, subresources) VALUES (4,  1,  4, 0,    0, NULL, '');
