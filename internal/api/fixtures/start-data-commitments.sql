CREATE OR REPLACE FUNCTION unix(i integer) RETURNS timestamp AS $$ SELECT TO_TIMESTAMP(i) AT LOCAL $$ LANGUAGE SQL;

INSERT INTO services (id, type, scraped_at, next_scrape_at, liquid_version) VALUES (1, 'first', UNIX(1000), UNIX(2000), 1);
INSERT INTO services (id, type, scraped_at, next_scrape_at, liquid_version) VALUES (2, 'second', UNIX(1000), UNIX(2000), 1);
INSERT INTO services (id, type, scraped_at, next_scrape_at, liquid_version) VALUES (3, 'third', UNIX(1000), UNIX(2000), 1);
INSERT INTO services (id, type, scraped_at, next_scrape_at, liquid_version) VALUES (4, 'fourth', UNIX(1000), UNIX(2000), 1);

-- resources and az_resources have entries for the resources where commitments are enabled in the config
INSERT INTO resources (id, service_id, name, liquid_version, unit, topology, has_capacity, has_quota, needs_resource_demand, path, handles_commitments) VALUES (1, 1, 'capacity', 1, 'B', 'az-aware', TRUE, TRUE, TRUE, 'first/capacity', TRUE);
INSERT INTO resources (id, service_id, name, liquid_version, unit, topology, has_capacity, has_quota, needs_resource_demand, path, handles_commitments) VALUES (2, 2, 'capacity', 1, 'B', 'az-aware', TRUE, TRUE, TRUE, 'second/capacity', TRUE);
INSERT INTO resources (id, service_id, name, liquid_version, topology, has_quota, path, handles_commitments) VALUES (3, 1, 'things', 1, 'flat', TRUE, 'first/things', TRUE);
INSERT INTO resources (id, service_id, name, liquid_version, topology, has_quota, path, handles_commitments) VALUES (4, 2, 'things', 1, 'flat', TRUE, 'second/things', TRUE);
INSERT INTO resources (id, service_id, name, liquid_version, unit, topology, has_quota, path, handles_commitments) VALUES (5, 3, 'capacity_c32', 1, 'B', 'flat', TRUE, 'third/capacity_c32', TRUE);
INSERT INTO resources (id, service_id, name, liquid_version, unit, topology, has_quota, path, handles_commitments) VALUES (6, 3, 'capacity_c48', 1, 'B', 'flat', TRUE, 'third/capacity_c48', TRUE);
INSERT INTO resources (id, service_id, name, liquid_version, unit, topology, has_quota, path, handles_commitments) VALUES (7, 3, 'capacity_c96', 1, 'B', 'flat', TRUE, 'third/capacity_c96', TRUE);
INSERT INTO resources (id, service_id, name, liquid_version, topology, has_quota, path, handles_commitments) VALUES (8, 3, 'capacity_c120', 1, 'flat', TRUE, 'third/capacity_c120', TRUE);
INSERT INTO resources (id, service_id, name, liquid_version, topology, has_quota, path, handles_commitments) VALUES (9, 3, 'capacity2_c144', 1, 'flat', TRUE, 'third/capacity2_c144', TRUE);
INSERT INTO resources (id, service_id, name, liquid_version, unit, topology, path, handles_commitments) VALUES (10, 2, 'other', 1, 'B', 'az-aware', 'second/other', TRUE);
INSERT INTO resources (id, service_id, name, liquid_version, unit, topology, path, handles_commitments) VALUES (11, 1, 'other', 1, 'B', 'az-aware', 'first/other', TRUE);
-- only used for conversion tests
INSERT INTO resources (id, service_id, name, liquid_version, unit, topology, path, handles_commitments) VALUES (12, 4, 'capacity_a', 1, 'B', 'az-aware', 'fourth/capacity_a', TRUE);
INSERT INTO resources (id, service_id, name, liquid_version, unit, topology, path, handles_commitments) VALUES (13, 4, 'capacity_b', 1, 'B', 'az-aware', 'fourth/capacity_b', TRUE);


INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity, path) VALUES (1, 1, 'any', 0, 0, '', 0, 'first/capacity/any');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity, path) VALUES (2, 1, 'az-one', 10, 6, '', 10, 'first/capacity/az-one');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity, path) VALUES (3, 1, 'az-two', 20, 6, '', 20, 'first/capacity/az-two');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity, path) VALUES (4, 2, 'any', 0, 0, '', 0, 'second/capacity/any');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity, path) VALUES (5, 2, 'az-one', 30, 6, '', 30, 'second/capacity/az-one');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity, path) VALUES (6, 2, 'az-two', 40, 6, '', 40, 'second/capacity/az-two');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity, path) VALUES (7, 3, 'any', 0, 0, '', 0, 'first/things/any');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity, path) VALUES (8, 4, 'any', 0, 0, '', 0, 'second/things/any');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity, path) VALUES (9, 5, 'any', 0, 0, '', 0, 'third/capacity_c32/any');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity, path) VALUES (10, 6, 'any', 0, 0, '', 0, 'third/capacity_c48/any');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity, path) VALUES (11, 7, 'any', 0, 0, '', 0, 'third/capacity_c96/any');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity, path) VALUES (12, 8, 'any', 0, 0, '', 0, 'third/capacity_c120/any');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity, path) VALUES (13, 9, 'any', 0, 0, '', 0, 'third/capacity2_c144/any');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity, path) VALUES (14, 10, 'any', 0, 0, '', 0, 'second/other/any');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity, path) VALUES (15, 10, 'az-one', 0, 0, '', 0, 'second/other/az-one');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity, path) VALUES (16, 10, 'az-two', 0, 0, '', 0, 'second/other/az-two');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity, path) VALUES (17, 11, 'any', 0, 0, '', 0, 'first/other/any');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity, path) VALUES (18, 11, 'az-one', 0, 0, '', 0, 'first/other/az-one');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity, path) VALUES (19, 11, 'az-two', 0, 0, '', 0, 'first/other/az-two');
-- only used for conversion tests
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity, path) VALUES (20, 12, 'any', 0, 0, '', 0, 'fourth/capacity_a/any');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity, path) VALUES (21, 12, 'az-one', 10, 6, '', 10, 'fourth/capacity_a/az-one');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity, path) VALUES (22, 12, 'az-two', 20, 6, '', 20, 'fourth/capacity_a/az-two');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity, path) VALUES (23, 13, 'any', 0, 0, '', 0, 'fourth/capacity_b/any');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity, path) VALUES (24, 13, 'az-one', 30, 6, '', 30, 'fourth/capacity_b/az-one');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity, path) VALUES (25, 13, 'az-two', 40, 6, '', 40, 'fourth/capacity_b/az-two');


-- two domains (default setup for StaticDiscoveryPlugin)
INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');
INSERT INTO domains (id, name, uuid) VALUES (2, 'france',  'uuid-for-france');

-- three projects (default setup for StaticDiscoveryPlugin)
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (3, 2, 'paris', 'uuid-for-paris', 'uuid-for-france');

-- project_services is fully populated (as ensured by the collector's consistency check)
INSERT INTO project_services (id, project_id, service_id, scraped_at, checked_at) VALUES (1, 1, 1,  UNIX(11), UNIX(11));
INSERT INTO project_services (id, project_id, service_id, scraped_at, checked_at) VALUES (2, 1, 2, UNIX(22), UNIX(22));
INSERT INTO project_services (id, project_id, service_id, scraped_at, checked_at) VALUES (3, 2, 1,  UNIX(33), UNIX(33));
INSERT INTO project_services (id, project_id, service_id, scraped_at, checked_at) VALUES (4, 2, 2, UNIX(44), UNIX(44));
INSERT INTO project_services (id, project_id, service_id, scraped_at, checked_at) VALUES (5, 3, 1,  UNIX(55), UNIX(55));
INSERT INTO project_services (id, project_id, service_id, scraped_at, checked_at) VALUES (6, 3, 2, UNIX(66), UNIX(66));
-- only used for conversion tests
INSERT INTO project_services (id, project_id, service_id, scraped_at, checked_at) VALUES (7, 1, 4,  UNIX(77), UNIX(77));
INSERT INTO project_services (id, project_id, service_id, scraped_at, checked_at) VALUES (8, 2, 4,  UNIX(88), UNIX(88));

-- project_resources contains only boring placeholder values
-- berlin
INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (1,  1, 3,   10, 10);
INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (2,  1, 1, 10, 10);
INSERT INTO project_resources (id, project_id, resource_id) VALUES (3,  1, 11);
INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (4,  1, 4,   10, 10);
INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (5,  1, 2, 10, 10);
INSERT INTO project_resources (id, project_id, resource_id) VALUES (6,  1, 10);
-- dresden
INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (7,  2, 3,   10, 10);
INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (8,  2, 1, 10, 10);
INSERT INTO project_resources (id, project_id, resource_id) VALUES (9,  2, 11);
INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (10,  2, 4,   10, 10);
INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (11,  2, 2, 10, 10);
INSERT INTO project_resources (id, project_id, resource_id) VALUES (12,  2, 10);
-- paris
INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (13,  3, 3,   10, 10);
INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (14,  3, 1, 10, 10);
INSERT INTO project_resources (id, project_id, resource_id) VALUES (15,  3, 11);
INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (16,  3, 4,   10, 10);
INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (17,  3, 2, 10, 10);
INSERT INTO project_resources (id, project_id, resource_id) VALUES (18,  3, 10);
-- only used for conversion tests
INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (19,  1, 12, 10, 10);
INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (20,  1, 13, 10, 10);
INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (21,  2, 12, 10, 10);
INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (22,  2, 13, 10, 10);
INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (23,  3, 12, 10, 10);
INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (24,  3, 13, 10, 10);

-- project_az_resources has "things" as non-AZ-aware and "capacity" as AZ-aware with an even split
-- NOTE: AZ-aware resources also have an entry for AZ "any" with 0 usage
--       (this is consistent with what Scrape does, and reporting should ignore those entries)
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (1,  1,  7,   4);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (2,  1,  1,   0);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (3,  1,  2,   2);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (4,  1,  3,   2);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (5,  1,  17,  0);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (6,  1,  18,  1);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (7,  1,  19,  1);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (8,  1,  8,   4);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (9,  1,  4,   0);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (10, 1,  5,   2);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (11, 1,  6,   2);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (12, 1,  14,  0);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (13, 1,  15,  1);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (14, 1,  16,  1);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (15, 2,  7,   4);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (16, 2,  1,   0);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (17, 2,  2,   2);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (18, 2,  3,   2);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (19, 2,  17,  0);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (20, 2,  18,  1);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (21, 2,  19,  1);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (22, 2,  8,   4);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (23, 2,  4,   0);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (24, 2,  5,   2);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (25, 2,  6,   2);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (26, 2,  14,  0);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (27, 2,  15,  1);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (28, 2,  16,  1);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (29, 3,  7,   4);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (30, 3,  1,   0);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (31, 3,  2,   2);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (32, 3,  3,   2);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (33, 3,  17,  0);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (34, 3,  18,  1);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (35, 3,  19,  1);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (36, 3,  8,   4);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (37, 3,  4,   0);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (38, 3,  5,   2);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (39, 3,  6,   2);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (40, 3,  14,  0);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (41, 3,  15,  1);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (42, 3,  16,  1);
-- only used for conversion tests
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (43 ,1,  20,   0);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (44 ,1,  21,   2);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (45 ,1,  22,   2);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (46 ,1,  23,   0);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (47 ,1,  24,   2);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (48 ,1,  25,   2);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (49 ,2,  20,   0);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (50 ,2,  21,   2);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (51 ,2,  22,   2);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (52 ,2,  23,   0);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (53 ,2,  24,   2);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (54 ,2,  25,   2);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (55 ,3,  20,   0);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (56 ,3,  21,   2);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (57 ,3,  22,   2);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (58 ,3,  23,   0);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (59 ,3,  24,   2);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (60 ,3,  25,   2);
-- project_rates is empty: no rates configured
