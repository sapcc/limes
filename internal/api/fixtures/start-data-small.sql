-- This start-data contains exactly one project and one service, with all capacity/quota/usage values at 0.
-- It can be used as a base to set up isolated tests for individual reporting features.

CREATE OR REPLACE FUNCTION unix(i integer) RETURNS timestamp AS $$ SELECT TO_TIMESTAMP(i) AT TIME ZONE 'Etc/UTC' $$ LANGUAGE SQL;

INSERT INTO cluster_services (id, type, scraped_at, next_scrape_at, liquid_version) VALUES (1, 'first', UNIX(1000), UNIX(2000), 1);

INSERT INTO cluster_resources (id, service_id, name, liquid_version) VALUES (1, 1, 'things', 1);
INSERT INTO cluster_resources (id, service_id, name, liquid_version) VALUES (2, 1, 'capacity', 1);

-- "capacity" is modeled as AZ-aware, "things" is not
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities) VALUES (1, 1, 'any', 0, 0, '');
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities) VALUES (2, 2, 'az-one', 0, 0, '');
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities) VALUES (3, 2, 'az-two', 0, 0, '');

INSERT INTO domains (id, name, uuid) VALUES (1, 'domainone', 'uuid-for-domainone');

INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'projectone', 'uuid-for-projectone', 'uuid-for-domainone');

INSERT INTO project_services (id, project_id, type, scraped_at, rates_scraped_at, checked_at, rates_checked_at) VALUES (1, 1, 'first', UNIX(11), UNIX(12), UNIX(11), UNIX(12));

INSERT INTO project_resources (id, service_id, name, quota, backend_quota) VALUES (1,  1, 'things',   0, 0);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota) VALUES (2,  1, 'capacity', 0, 0);

-- "capacity" is modeled as AZ-aware, "things" is not
INSERT INTO project_az_resources (id, resource_id, az, quota, usage, physical_usage, subresources) VALUES (1,  1,  'any',    0,    0, NULL, '');
INSERT INTO project_az_resources (id, resource_id, az, quota, usage, physical_usage, subresources) VALUES (2,  2,  'any',    0,    0, NULL, '');
INSERT INTO project_az_resources (id, resource_id, az, quota, usage, physical_usage, subresources) VALUES (3,  2,  'az-one', 0,    0, NULL, '');
INSERT INTO project_az_resources (id, resource_id, az, quota, usage, physical_usage, subresources) VALUES (4,  2,  'az-two', 0,    0, NULL, '');
