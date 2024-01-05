CREATE OR REPLACE FUNCTION unix(i integer) RETURNS timestamp AS $$ SELECT TO_TIMESTAMP(i) AT TIME ZONE 'Etc/UTC' $$ LANGUAGE SQL;

INSERT INTO cluster_capacitors (capacitor_id, scraped_at, next_scrape_at) VALUES ('unittest', UNIX(1000), UNIX(2000));

INSERT INTO cluster_services (id, type) VALUES (1, 'first');
INSERT INTO cluster_services (id, type) VALUES (2, 'second');

-- cluster_resources and cluster_az_resources have entries for the resources where commitments are enabled in the config
INSERT INTO cluster_resources (id, service_id, name, capacitor_id) VALUES (1, 1, 'capacity', 'unittest');
INSERT INTO cluster_resources (id, service_id, name, capacitor_id) VALUES (2, 2, 'capacity', 'unittest');

INSERT INTO cluster_az_resources (resource_id, az, raw_capacity, usage, subcapacities) VALUES (1, 'az-one', 10, 6, '');
INSERT INTO cluster_az_resources (resource_id, az, raw_capacity, usage, subcapacities) VALUES (1, 'az-two', 20, 6, '');
INSERT INTO cluster_az_resources (resource_id, az, raw_capacity, usage, subcapacities) VALUES (2, 'az-one', 30, 6, '');
INSERT INTO cluster_az_resources (resource_id, az, raw_capacity, usage, subcapacities) VALUES (2, 'az-two', 40, 6, '');

-- two domains (default setup for StaticDiscoveryPlugin)
INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');
INSERT INTO domains (id, name, uuid) VALUES (2, 'france',  'uuid-for-france');

-- domain_services is fully populated (as ensured by the collector's consistency check)
INSERT INTO domain_services (id, domain_id, type) VALUES (1, 1, 'first');
INSERT INTO domain_services (id, domain_id, type) VALUES (2, 1, 'second');
INSERT INTO domain_services (id, domain_id, type) VALUES (3, 2, 'first');
INSERT INTO domain_services (id, domain_id, type) VALUES (4, 2, 'second');

-- domain_resources is empty: the commitment tests do not care about domain quota

-- three projects (default setup for StaticDiscoveryPlugin)
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany', FALSE);
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin', FALSE);
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (3, 2, 'paris', 'uuid-for-paris', 'uuid-for-france', FALSE);

-- project_services is fully populated (as ensured by the collector's consistency check)
INSERT INTO project_services (id, project_id, type, scraped_at, checked_at) VALUES (1, 1, 'first',  UNIX(11), UNIX(11));
INSERT INTO project_services (id, project_id, type, scraped_at, checked_at) VALUES (2, 1, 'second', UNIX(22), UNIX(22));
INSERT INTO project_services (id, project_id, type, scraped_at, checked_at) VALUES (3, 2, 'first',  UNIX(33), UNIX(33));
INSERT INTO project_services (id, project_id, type, scraped_at, checked_at) VALUES (4, 2, 'second', UNIX(44), UNIX(44));
INSERT INTO project_services (id, project_id, type, scraped_at, checked_at) VALUES (5, 3, 'first',  UNIX(55), UNIX(55));
INSERT INTO project_services (id, project_id, type, scraped_at, checked_at) VALUES (6, 3, 'second', UNIX(66), UNIX(66));

-- project_resources contains only boring placeholder values
-- berlin
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (1,  1, 'things',   10, 10, 10);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (2,  1, 'capacity', 10, 10, 10);
INSERT INTO project_resources (id, service_id, name) VALUES (3,  1, 'capacity_portion');
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (4,  2, 'things',   10, 10, 10);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (5,  2, 'capacity', 10, 10, 10);
INSERT INTO project_resources (id, service_id, name) VALUES (6,  2, 'capacity_portion');
-- dresden
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (7,  3, 'things',   10, 10, 10);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (8,  3, 'capacity', 10, 10, 10);
INSERT INTO project_resources (id, service_id, name) VALUES (9,  3, 'capacity_portion');
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (10, 4, 'things',   10, 10, 10);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (11, 4, 'capacity', 10, 10, 10);
INSERT INTO project_resources (id, service_id, name) VALUES (12, 4, 'capacity_portion');
-- paris
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (13, 5, 'things',   10, 10, 10);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (14, 5, 'capacity', 10, 10, 10);
INSERT INTO project_resources (id, service_id, name) VALUES (15, 5, 'capacity_portion');
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (16, 6, 'things',   10, 10, 10);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (17, 6, 'capacity', 10, 10, 10);
INSERT INTO project_resources (id, service_id, name) VALUES (18, 6, 'capacity_portion');

-- project_az_resources has "things" as non-AZ-aware and "capacity" as AZ-aware with an even split
-- NOTE: AZ-aware resources also have an entry for AZ "any" with 0 usage
--       (this is consistent with what Scrape does, and reporting should ignore those entries)
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (1, 'any', 4);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (2, 'any',    0);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (2, 'az-one', 2);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (2, 'az-two', 2);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (3, 'any',    0);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (3, 'az-one', 1);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (3, 'az-two', 1);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (4, 'any', 4);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (5, 'any',    0);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (5, 'az-one', 2);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (5, 'az-two', 2);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (6, 'any',    0);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (6, 'az-one', 1);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (6, 'az-two', 1);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (7, 'any', 4);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (8, 'any',    0);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (8, 'az-one', 2);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (8, 'az-two', 2);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (9, 'any',    0);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (9, 'az-one', 1);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (9, 'az-two', 1);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (10, 'any', 4);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (11, 'any',    0);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (11, 'az-one', 2);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (11, 'az-two', 2);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (12, 'any',    0);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (12, 'az-one', 1);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (12, 'az-two', 1);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (13, 'any', 4);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (14, 'any',    0);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (14, 'az-one', 2);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (14, 'az-two', 2);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (15, 'any',    0);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (15, 'az-one', 1);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (15, 'az-two', 1);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (16, 'any', 4);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (17, 'any',    0);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (17, 'az-one', 2);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (17, 'az-two', 2);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (18, 'any',    0);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (18, 'az-one', 1);
INSERT INTO project_az_resources (resource_id, az, usage) VALUES (18, 'az-two', 1);

-- project_rates is empty: no rates configured
