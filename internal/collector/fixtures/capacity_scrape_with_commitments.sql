-- This DB forms the baseline for Test_ScanCapacityWithCommitments.

CREATE OR REPLACE FUNCTION UNIX(i integer) RETURNS timestamp AS $$ SELECT TO_TIMESTAMP(i) AT LOCAL $$ LANGUAGE SQL;

-- capacity scrape needs these as a baseline (these are usually created by the CheckConsistencyJob)
INSERT INTO services (id, type, next_scrape_at, scrape_duration_secs) VALUES (1, 'first',  UNIX(0), 5);
INSERT INTO services (id, type, next_scrape_at, scrape_duration_secs) VALUES (2, 'second', UNIX(0), 5);

-- capacity scrape would fill resources and az_resources
-- on its own, but we do it here to minimize the inline diffs in the test code
INSERT INTO resources (id, service_id, name, path) VALUES (1, 1, 'things', 'first/things');
INSERT INTO resources (id, service_id, name, path) VALUES (2, 1, 'capacity', 'first/capacity');
INSERT INTO resources (id, service_id, name, path) VALUES (3, 2, 'things', 'second/things');
INSERT INTO resources (id, service_id, name, path) VALUES (4, 2, 'capacity', 'second/capacity');

INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, last_nonzero_raw_capacity, path) VALUES (1, 1, 'any', 0, 0, null, 'first/things/any');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, last_nonzero_raw_capacity, path) VALUES (2, 1, 'az-one', 42, 8, 42, 'first/things/az-one');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, last_nonzero_raw_capacity, path) VALUES (3, 1, 'az-two', 42, 8, 42, 'first/things/az-two');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, last_nonzero_raw_capacity, path) VALUES (4, 2, 'any', 0, 0, null, 'first/capacity/any');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, last_nonzero_raw_capacity, path) VALUES (5, 2, 'az-one', 42, 8, 42, 'first/capacity/az-one');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, last_nonzero_raw_capacity, path) VALUES (6, 2, 'az-two', 42, 8, 42, 'first/capacity/az-two');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, last_nonzero_raw_capacity, path) VALUES (7, 3, 'any', 0, 0, null, 'second/things/any');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, last_nonzero_raw_capacity, path) VALUES (8, 3, 'az-one', 23, 4, 23, 'second/things/az-one');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, last_nonzero_raw_capacity, path) VALUES (9, 3, 'az-two', 23, 4, 23, 'second/things/az-two');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, last_nonzero_raw_capacity, path) VALUES (10, 4, 'any', 0, 0, null, 'second/capacity/any');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, last_nonzero_raw_capacity, path) VALUES (11, 4, 'az-one', 23, 4, 23, 'second/capacity/az-one');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, last_nonzero_raw_capacity, path) VALUES (12, 4, 'az-two', 23, 4, 23, 'second/capacity/az-two');
-- unused, but will be created by the collector anyways
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, last_nonzero_raw_capacity, path) VALUES (13, 1, 'unknown', 0, 0, null, 'first/things/unknown');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, last_nonzero_raw_capacity, path) VALUES (14, 2, 'unknown', 0, 0, null, 'first/capacity/unknown');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, last_nonzero_raw_capacity, path) VALUES (15, 3, 'unknown', 0, 0, null, 'second/things/unknown');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, last_nonzero_raw_capacity, path) VALUES (16, 4, 'unknown', 0, 0, null, 'second/capacity/unknown');

-- one domain
INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');

-- two projects
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin');

-- project_services is fully populated (as ensured by the collector's consistency check)
INSERT INTO project_services (id, project_id, service_id) VALUES (1, 1, 1);
INSERT INTO project_services (id, project_id, service_id) VALUES (2, 1, 2);
INSERT INTO project_services (id, project_id, service_id) VALUES (3, 2, 1);
INSERT INTO project_services (id, project_id, service_id) VALUES (4, 2, 2);

-- no quota set here because commitment confirmation does not care about quota
INSERT INTO project_resources (id, project_id, resource_id) VALUES (1,  1, 1);
INSERT INTO project_resources (id, project_id, resource_id) VALUES (2,  1, 2);
INSERT INTO project_resources (id, project_id, resource_id) VALUES (3,  1, 3);
INSERT INTO project_resources (id, project_id, resource_id) VALUES (4,  1, 4);
INSERT INTO project_resources (id, project_id, resource_id) VALUES (5,  2, 1);
INSERT INTO project_resources (id, project_id, resource_id) VALUES (6,  2, 2);
INSERT INTO project_resources (id, project_id, resource_id) VALUES (7, 2, 3);
INSERT INTO project_resources (id, project_id, resource_id) VALUES (8, 2, 4);

-- */things resources do not have commitments, so they are boring and we don't need to care
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (1, 1,  1,    0);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (2, 1,  7,    0);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (3, 2,  1,    0);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (4, 2, 7,    0);

-- part 2: */capacity resources can have commitments, so we have some large
-- usage values here to see that these block commitments on other projects, but
-- not on the project itself
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (5, 1, 4, 0);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (6, 1, 5, 1);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (7, 1, 6, 250);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (8, 1,  10,    0);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (9, 1,  11, 1);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (10, 1, 12, 1);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (11, 2, 4,    0);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (12, 2, 5, 1);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (13, 2, 6, 1);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (14, 2, 10,    0);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (15, 2, 11, 1);
INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (16, 2, 12, 1);

-- project_commitments has multiple testcases that invoke in the test by skipping to the respective confirm_by time
-- (the confirm_by and expires_at timestamps are all aligned on day boundaries, i.e. T = 86400 * N for some integer N)

-- day 1: just a boring commitment that easily fits in the available capacity
INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, amount, created_at, creator_uuid, creator_name, confirm_by, duration, expires_at, state, creation_context_json) VALUES (1, '00000000-0000-0000-0000-000000000001', 1, 5, 10, UNIX(0), 'dummy', 'dummy', UNIX(86400), '10 days', UNIX(950400), 'planned', '{}'::jsonb);

-- day 2: very large commitments that exceed the raw capacity; only the one on "first" works because that service has a large overcommit factor
INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, amount, created_at, creator_uuid, creator_name, confirm_by, duration, expires_at, state, creation_context_json) VALUES (2, '00000000-0000-0000-0000-000000000002', 1, 5, 100, UNIX(0), 'dummy', 'dummy', UNIX(172800), '10 days', UNIX(1036800), 'planned', '{}'::jsonb);
INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, amount, created_at, creator_uuid, creator_name, confirm_by, duration, expires_at, state, creation_context_json) VALUES (3, '00000000-0000-0000-0000-000000000003', 1, 11, 100, UNIX(0), 'dummy', 'dummy', UNIX(172800), '10 days', UNIX(1036800), 'planned', '{}'::jsonb);

-- day 3: a bunch of small commitments with different timestamps, to test confirmation order in two ways:
--
-- 1. ID=3 does not block these commitments even though it is on the same resource and AZ
-- 2. we cannot confirm all of these; which ones are confirmed demonstrates the order of consideration
INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, amount, created_at, creator_uuid, creator_name, confirm_by, duration, expires_at, state, creation_context_json) VALUES (4, '00000000-0000-0000-0000-000000000004', 2, 11, 10, UNIX(1), 'dummy', 'dummy', UNIX(259202), '10 days', UNIX(1123200), 'planned', '{}'::jsonb);
INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, amount, created_at, creator_uuid, creator_name, confirm_by, duration, expires_at, state, creation_context_json) VALUES (5, '00000000-0000-0000-0000-000000000005', 2, 11, 10, UNIX(2), 'dummy', 'dummy', UNIX(259201), '10 days', UNIX(1123200), 'planned', '{}'::jsonb);
INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, amount, created_at, creator_uuid, creator_name, confirm_by, duration, expires_at, state, creation_context_json) VALUES (6, '00000000-0000-0000-0000-000000000006', 2, 11, 10, UNIX(3), 'dummy', 'dummy', UNIX(259200), '10 days', UNIX(1123200), 'planned', '{}'::jsonb);

-- day 4: test confirmation that is (or is not) blocked by existing usage in other projects (on a capacity of 420, there is already 250 usage in berlin, so only berlin can confirm a commitment for amount = 300, even though dresden asked first)
INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, amount, created_at, creator_uuid, creator_name, confirm_by, duration, expires_at, state, creation_context_json) VALUES (7, '00000000-0000-0000-0000-000000000007', 2, 6, 300, UNIX(1), 'dummy', 'dummy', UNIX(345600), '10 days', UNIX(1209600), 'planned', '{}'::jsonb);
INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, amount, created_at, creator_uuid, creator_name, confirm_by, duration, expires_at, state, creation_context_json) VALUES (8, '00000000-0000-0000-0000-000000000008', 1, 6, 300, UNIX(2), 'dummy', 'dummy', UNIX(345600), '10 days', UNIX(1209600), 'planned', '{}'::jsonb);

-- day 5: test commitments that cannot be confirmed until the previous commitment expires (ID=9 is confirmed, and then ID=10 cannot be confirmed until ID=9 expires because ID=9 blocks absolutely all available capacity in that resource and AZ)
INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, amount, created_at, creator_uuid, creator_name, confirm_by, duration, expires_at, state, creation_context_json) VALUES (9, '00000000-0000-0000-0000-000000000009',  1, 12,  22, UNIX(1), 'dummy', 'dummy', UNIX(432000), '1 hour', UNIX(435600),  'planned', '{}'::jsonb);
INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, amount, created_at, creator_uuid, creator_name, confirm_by, duration, expires_at, state, creation_context_json) VALUES (10, '00000000-0000-0000-0000-000000000010', 2, 12, 2, UNIX(2), 'dummy', 'dummy', UNIX(432000), '10 days', UNIX(1296000), 'planned', '{}'::jsonb);
