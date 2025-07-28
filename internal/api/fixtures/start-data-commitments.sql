CREATE OR REPLACE FUNCTION unix(i integer) RETURNS timestamp AS $$ SELECT TO_TIMESTAMP(i) AT LOCAL $$ LANGUAGE SQL;

INSERT INTO cluster_services (id, type, scraped_at, next_scrape_at, liquid_version) VALUES (1, 'first', UNIX(1000), UNIX(2000), 1);
INSERT INTO cluster_services (id, type, scraped_at, next_scrape_at, liquid_version) VALUES (2, 'second', UNIX(1000), UNIX(2000), 1);
INSERT INTO cluster_services (id, type, scraped_at, next_scrape_at, liquid_version) VALUES (3, 'third', UNIX(1000), UNIX(2000), 1);

-- cluster_resources and cluster_az_resources have entries for the resources where commitments are enabled in the config
INSERT INTO cluster_resources (id, service_id, name, liquid_version, unit, topology, has_capacity, has_quota, needs_resource_demand) VALUES (1, 1, 'capacity', 1, 'B', 'az-aware', TRUE, TRUE, TRUE);
INSERT INTO cluster_resources (id, service_id, name, liquid_version, unit, topology, has_capacity, has_quota, needs_resource_demand) VALUES (2, 2, 'capacity', 1, 'B', 'az-aware', TRUE, TRUE, TRUE);
INSERT INTO cluster_resources (id, service_id, name, liquid_version, topology, has_quota) VALUES (3, 1, 'things', 1, 'flat', TRUE);
INSERT INTO cluster_resources (id, service_id, name, liquid_version, topology, has_quota) VALUES (4, 2, 'things', 1, 'flat', TRUE);
INSERT INTO cluster_resources (id, service_id, name, liquid_version, unit, topology, has_quota) VALUES (5, 3, 'capacity_c32', 1, 'B', 'flat', TRUE);
INSERT INTO cluster_resources (id, service_id, name, liquid_version, unit, topology, has_quota) VALUES (6, 3, 'capacity_c48', 1, 'B', 'flat', TRUE);
INSERT INTO cluster_resources (id, service_id, name, liquid_version, unit, topology, has_quota) VALUES (7, 3, 'capacity_c96', 1, 'B', 'flat', TRUE);
INSERT INTO cluster_resources (id, service_id, name, liquid_version, topology, has_quota) VALUES (8, 3, 'capacity_c120', 1, 'flat', TRUE);
INSERT INTO cluster_resources (id, service_id, name, liquid_version, topology, has_quota) VALUES (9, 3, 'capacity2_c144', 1, 'flat', TRUE);
INSERT INTO cluster_resources (id, service_id, name, liquid_version, unit, topology) VALUES (10, 2, 'other', 1, 'B', 'az-aware');
INSERT INTO cluster_resources (id, service_id, name, liquid_version, unit, topology) VALUES (11, 1, 'other', 1, 'B', 'az-aware');


INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity) VALUES (1, 1, 'any', 0, 0, '', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity) VALUES (2, 1, 'az-one', 10, 6, '', 10);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity) VALUES (3, 1, 'az-two', 20, 6, '', 20);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity) VALUES (4, 2, 'any', 0, 0, '', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity) VALUES (5, 2, 'az-one', 30, 6, '', 30);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity) VALUES (6, 2, 'az-two', 40, 6, '', 40);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity) VALUES (7, 3, 'any', 0, 0, '', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity) VALUES (8, 4, 'any', 0, 0, '', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity) VALUES (9, 5, 'any', 0, 0, '', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity) VALUES (10, 6, 'any', 0, 0, '', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity) VALUES (11, 7, 'any', 0, 0, '', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity) VALUES (12, 8, 'any', 0, 0, '', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity) VALUES (13, 9, 'any', 0, 0, '', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity) VALUES (14, 10, 'any', 0, 0, '', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity) VALUES (15, 10, 'az-one', 0, 0, '', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity) VALUES (16, 10, 'az-two', 0, 0, '', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity) VALUES (17, 11, 'any', 0, 0, '', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity) VALUES (18, 11, 'az-one', 0, 0, '', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity) VALUES (19, 11, 'az-two', 0, 0, '', 0);

-- two domains (default setup for StaticDiscoveryPlugin)
INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');
INSERT INTO domains (id, name, uuid) VALUES (2, 'france',  'uuid-for-france');

-- three projects (default setup for StaticDiscoveryPlugin)
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (3, 2, 'paris', 'uuid-for-paris', 'uuid-for-france');

-- project_services is fully populated (as ensured by the collector's consistency check)
INSERT INTO project_services_v2 (id, project_id, service_id, scraped_at, checked_at) VALUES (1, 1, 1,  UNIX(11), UNIX(11));
INSERT INTO project_services_v2 (id, project_id, service_id, scraped_at, checked_at) VALUES (2, 1, 2, UNIX(22), UNIX(22));
INSERT INTO project_services_v2 (id, project_id, service_id, scraped_at, checked_at) VALUES (3, 2, 1,  UNIX(33), UNIX(33));
INSERT INTO project_services_v2 (id, project_id, service_id, scraped_at, checked_at) VALUES (4, 2, 2, UNIX(44), UNIX(44));
INSERT INTO project_services_v2 (id, project_id, service_id, scraped_at, checked_at) VALUES (5, 3, 1,  UNIX(55), UNIX(55));
INSERT INTO project_services_v2 (id, project_id, service_id, scraped_at, checked_at) VALUES (6, 3, 2, UNIX(66), UNIX(66));

-- project_resources contains only boring placeholder values
-- berlin
INSERT INTO project_resources_v2 (id, project_id, resource_id, quota, backend_quota) VALUES (1,  1, 3,   10, 10);
INSERT INTO project_resources_v2 (id, project_id, resource_id, quota, backend_quota) VALUES (2,  1, 1, 10, 10);
INSERT INTO project_resources_v2 (id, project_id, resource_id) VALUES (3,  1, 11);
INSERT INTO project_resources_v2 (id, project_id, resource_id, quota, backend_quota) VALUES (4,  1, 4,   10, 10);
INSERT INTO project_resources_v2 (id, project_id, resource_id, quota, backend_quota) VALUES (5,  1, 2, 10, 10);
INSERT INTO project_resources_v2 (id, project_id, resource_id) VALUES (6,  1, 10);
-- dresden
INSERT INTO project_resources_v2 (id, project_id, resource_id, quota, backend_quota) VALUES (7,  2, 3,   10, 10);
INSERT INTO project_resources_v2 (id, project_id, resource_id, quota, backend_quota) VALUES (8,  2, 1, 10, 10);
INSERT INTO project_resources_v2 (id, project_id, resource_id) VALUES (9,  2, 11);
INSERT INTO project_resources_v2 (id, project_id, resource_id, quota, backend_quota) VALUES (10,  2, 4,   10, 10);
INSERT INTO project_resources_v2 (id, project_id, resource_id, quota, backend_quota) VALUES (11,  2, 2, 10, 10);
INSERT INTO project_resources_v2 (id, project_id, resource_id) VALUES (12,  2, 10);
-- paris
INSERT INTO project_resources_v2 (id, project_id, resource_id, quota, backend_quota) VALUES (13,  3, 3,   10, 10);
INSERT INTO project_resources_v2 (id, project_id, resource_id, quota, backend_quota) VALUES (14,  3, 1, 10, 10);
INSERT INTO project_resources_v2 (id, project_id, resource_id) VALUES (15,  3, 11);
INSERT INTO project_resources_v2 (id, project_id, resource_id, quota, backend_quota) VALUES (16,  3, 4,   10, 10);
INSERT INTO project_resources_v2 (id, project_id, resource_id, quota, backend_quota) VALUES (17,  3, 2, 10, 10);
INSERT INTO project_resources_v2 (id, project_id, resource_id) VALUES (18,  3, 10);

-- project_az_resources has "things" as non-AZ-aware and "capacity" as AZ-aware with an even split
-- NOTE: AZ-aware resources also have an entry for AZ "any" with 0 usage
--       (this is consistent with what Scrape does, and reporting should ignore those entries)
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (1,  1,  7,   4);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (2,  1,  1,   0);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (3,  1,  2,   2);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (4,  1,  3,   2);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (5,  1,  17,  0);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (6,  1,  18,  1);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (7,  1,  19,  1);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (8,  1,  8,   4);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (9,  1,  4,   0);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (10, 1,  5,   2);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (11, 1,  6,   2);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (12, 1,  14,  0);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (13, 1,  15,  1);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (14, 1,  16,  1);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (15, 2,  7,   4);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (16, 2,  1,   0);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (17, 2,  2,   2);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (18, 2,  3,   2);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (19, 2,  17,  0);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (20, 2,  18,  1);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (21, 2,  19,  1);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (22, 2,  8,   4);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (23, 2,  4,   0);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (24, 2,  5,   2);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (25, 2,  6,   2);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (26, 2,  14,  0);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (27, 2,  15,  1);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (28, 2,  16,  1);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (29, 3,  7,   4);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (30, 3,  1,   0);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (31, 3,  2,   2);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (32, 3,  3,   2);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (33, 3,  17,  0);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (34, 3,  18,  1);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (35, 3,  19,  1);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (36, 3,  8,   4);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (37, 3,  4,   0);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (38, 3,  5,   2);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (39, 3,  6,   2);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (40, 3,  14,  0);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (41, 3,  15,  1);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage) VALUES (42, 3,  16,  1);

-- project_rates is empty: no rates configured
