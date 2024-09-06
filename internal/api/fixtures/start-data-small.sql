-- This start-data contains exactly one project and one service, with all capacity/quota/usage values at 0.
-- It can be used as a base to set up isolated tests for individual reporting features.

CREATE OR REPLACE FUNCTION unix(i integer) RETURNS timestamp AS $$ SELECT TO_TIMESTAMP(i) AT TIME ZONE 'Etc/UTC' $$ LANGUAGE SQL;

INSERT INTO cluster_capacitors (capacitor_id, scraped_at, next_scrape_at) VALUES ('first', UNIX(1000), UNIX(2000));

INSERT INTO cluster_services (id, type) VALUES (1, 'first');

INSERT INTO cluster_resources (id, service_id, name, capacitor_id) VALUES (1, 1, 'things', 'first');
INSERT INTO cluster_resources (id, service_id, name, capacitor_id) VALUES (2, 1, 'capacity', 'first');

-- "capacity" is modeled as AZ-aware, "things" is not
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities) VALUES (1, 1, 'any', 0, 0, '');
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities) VALUES (2, 2, 'az-one', 0, 0, '');
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities) VALUES (3, 2, 'az-two', 0, 0, '');

INSERT INTO domains (id, name, uuid) VALUES (1, 'domainone', 'uuid-for-domainone');

INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'projectone', 'uuid-for-projectone', 'uuid-for-domainone');

INSERT INTO project_services (id, project_id, type, scraped_at, rates_scraped_at, checked_at, rates_checked_at) VALUES (1, 1, 'first', UNIX(11), UNIX(12), UNIX(11), UNIX(12));

INSERT INTO project_resources (id, service_id, name, quota, backend_quota) VALUES (1,  1, 'things',   0, 0);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota) VALUES (2,  1, 'capacity', 0, 0);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota) VALUES (3,  1, 'capacity_portion', NULL, NULL);

-- "capacity" and "capacity_portion" are modeled as AZ-aware, "things" is not
INSERT INTO project_az_resources (id, resource_id, az, quota, usage, physical_usage, subresources) VALUES (1,  1,  'any',    0,    0, NULL, '');
INSERT INTO project_az_resources (id, resource_id, az, quota, usage, physical_usage, subresources) VALUES (2,  2,  'any',    0,    0, NULL, '');
INSERT INTO project_az_resources (id, resource_id, az, quota, usage, physical_usage, subresources) VALUES (3,  2,  'az-one', 0,    0, NULL, '');
INSERT INTO project_az_resources (id, resource_id, az, quota, usage, physical_usage, subresources) VALUES (4,  2,  'az-two', 0,    0, NULL, '');
INSERT INTO project_az_resources (id, resource_id, az, quota, usage, physical_usage, subresources) VALUES (5,  3,  'any',    NULL, 0, NULL, '');
INSERT INTO project_az_resources (id, resource_id, az, quota, usage, physical_usage, subresources) VALUES (6,  3,  'az-one', NULL, 0, NULL, '');
INSERT INTO project_az_resources (id, resource_id, az, quota, usage, physical_usage, subresources) VALUES (7,  3,  'az-two', NULL, 0, NULL, '');
