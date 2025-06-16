-- This DB forms the baseline for Test_ScanCapacityWithCommitments.

CREATE OR REPLACE FUNCTION unix(i integer) RETURNS timestamp AS $$ SELECT TO_TIMESTAMP(i) AT TIME ZONE 'Etc/UTC' $$ LANGUAGE SQL;

-- capacity scrape needs these as a baseline (these are usually created by the CheckConsistencyJob)
INSERT INTO cluster_services (id, type, next_scrape_at, scrape_duration_secs) VALUES (1, 'first',  UNIX(0), 5);
INSERT INTO cluster_services (id, type, next_scrape_at, scrape_duration_secs) VALUES (2, 'second', UNIX(0), 5);

-- capacity scrape would fill cluster_resources and cluster_az_resources
-- on its own, but we do it here to minimize the inline diffs in the test code
INSERT INTO cluster_resources (id, service_id, name) VALUES (1, 1, 'things');
INSERT INTO cluster_resources (id, service_id, name) VALUES (2, 1, 'capacity');
INSERT INTO cluster_resources (id, service_id, name) VALUES (3, 2, 'things');
INSERT INTO cluster_resources (id, service_id, name) VALUES (4, 2, 'capacity');

INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, last_nonzero_raw_capacity) VALUES (1, 1, 'az-one', 42, 8, 42);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, last_nonzero_raw_capacity) VALUES (2, 1, 'az-two', 42, 8, 42);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, last_nonzero_raw_capacity) VALUES (3, 2, 'az-one', 42, 8, 42);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, last_nonzero_raw_capacity) VALUES (4, 2, 'az-two', 42, 8, 42);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, last_nonzero_raw_capacity) VALUES (5, 3, 'az-one', 23, 4, 23);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, last_nonzero_raw_capacity) VALUES (6, 3, 'az-two', 23, 4, 23);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, last_nonzero_raw_capacity) VALUES (7, 4, 'az-one', 23, 4, 23);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, last_nonzero_raw_capacity) VALUES (8, 4, 'az-two', 23, 4, 23);

-- one domain
INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');

-- two projects
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin');

-- project_services is fully populated (as ensured by the collector's consistency check)
INSERT INTO project_services (id, project_id, type) VALUES (1, 1, 'first');
INSERT INTO project_services (id, project_id, type) VALUES (2, 1, 'second');
INSERT INTO project_services (id, project_id, type) VALUES (3, 2, 'first');
INSERT INTO project_services (id, project_id, type) VALUES (4, 2, 'second');

-- no quota set here because commitment confirmation does not care about quota
INSERT INTO project_resources (id, service_id, name) VALUES (1,  1, 'things');
INSERT INTO project_resources (id, service_id, name) VALUES (2,  1, 'capacity');
INSERT INTO project_resources (id, service_id, name) VALUES (3,  2, 'things');
INSERT INTO project_resources (id, service_id, name) VALUES (4,  2, 'capacity');
INSERT INTO project_resources (id, service_id, name) VALUES (5,  3, 'things');
INSERT INTO project_resources (id, service_id, name) VALUES (6,  3, 'capacity');
INSERT INTO project_resources (id, service_id, name) VALUES (7, 4, 'things');
INSERT INTO project_resources (id, service_id, name) VALUES (8, 4, 'capacity');

-- */things resources do not have commitments, so they are boring and we don't need to care
INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (1, 1,  'any',    0);
INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (2, 3,  'any',    0);
INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (3, 5,  'any',    0);
INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (4, 7, 'any',    0);

-- part 2: */capacity resources can have commitments, so we have some large
-- usage values here to see that these block commitments on other projects, but
-- not on the project itself
INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (5, 2,  'any',    0);
INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (6, 2,  'az-one', 1);
INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (7, 2,  'az-two', 250);
INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (8, 4,  'any',    0);
INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (9, 4,  'az-one', 1);
INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (10, 4,  'az-two', 1);
INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (11, 6,  'any',    0);
INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (12, 6,  'az-one', 1);
INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (13, 6,  'az-two', 1);
INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (14, 8, 'any',    0);
INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (15, 8, 'az-one', 1);
INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (16, 8, 'az-two', 1);

-- project_commitments has multiple testcases that invoke in the test by skipping to the respective confirm_by time
-- (the confirm_by and expires_at timestamps are all aligned on day boundaries, i.e. T = 86400 * N for some integer N)

-- day 1: just a boring commitment that easily fits in the available capacity
INSERT INTO project_commitments (id, uuid, az_resource_id, amount, created_at, creator_uuid, creator_name, confirm_by, duration, expires_at, state, creation_context_json) VALUES (1, '00000000-0000-0000-0000-000000000001', 6, 10, UNIX(0), 'dummy', 'dummy', UNIX(86400), '10 days', UNIX(950400), 'planned', '{}'::jsonb);

-- day 2: very large commitments that exceed the raw capacity; only the one on "first" works because that service has a large overcommit factor
INSERT INTO project_commitments (id, uuid, az_resource_id, amount, created_at, creator_uuid, creator_name, confirm_by, duration, expires_at, state, creation_context_json) VALUES (2, '00000000-0000-0000-0000-000000000002', 6, 100, UNIX(0), 'dummy', 'dummy', UNIX(172800), '10 days', UNIX(1036800), 'planned', '{}'::jsonb);
INSERT INTO project_commitments (id, uuid, az_resource_id, amount, created_at, creator_uuid, creator_name, confirm_by, duration, expires_at, state, creation_context_json) VALUES (3, '00000000-0000-0000-0000-000000000003', 9, 100, UNIX(0), 'dummy', 'dummy', UNIX(172800), '10 days', UNIX(1036800), 'planned', '{}'::jsonb);

-- day 3: a bunch of small commitments with different timestamps, to test confirmation order in two ways:
--
-- 1. ID=3 does not block these commitments even though it is on the same resource and AZ
-- 2. we cannot confirm all of these; which ones are confirmed demonstrates the order of consideration
INSERT INTO project_commitments (id, uuid, az_resource_id, amount, created_at, creator_uuid, creator_name, confirm_by, duration, expires_at, state, creation_context_json) VALUES (4, '00000000-0000-0000-0000-000000000004', 15, 10, UNIX(1), 'dummy', 'dummy', UNIX(259202), '10 days', UNIX(1123200), 'planned', '{}'::jsonb);
INSERT INTO project_commitments (id, uuid, az_resource_id, amount, created_at, creator_uuid, creator_name, confirm_by, duration, expires_at, state, creation_context_json) VALUES (5, '00000000-0000-0000-0000-000000000005', 15, 10, UNIX(2), 'dummy', 'dummy', UNIX(259201), '10 days', UNIX(1123200), 'planned', '{}'::jsonb);
INSERT INTO project_commitments (id, uuid, az_resource_id, amount, created_at, creator_uuid, creator_name, confirm_by, duration, expires_at, state, creation_context_json) VALUES (6, '00000000-0000-0000-0000-000000000006', 15, 10, UNIX(3), 'dummy', 'dummy', UNIX(259200), '10 days', UNIX(1123200), 'planned', '{}'::jsonb);

-- day 4: test confirmation that is (or is not) blocked by existing usage in other projects (on a capacity of 420, there is already 250 usage in berlin, so only berlin can confirm a commitment for amount = 300, even though dresden asked first)
INSERT INTO project_commitments (id, uuid, az_resource_id, amount, created_at, creator_uuid, creator_name, confirm_by, duration, expires_at, state, creation_context_json) VALUES (7, '00000000-0000-0000-0000-000000000007', 13, 300, UNIX(1), 'dummy', 'dummy', UNIX(345600), '10 days', UNIX(1209600), 'planned', '{}'::jsonb);
INSERT INTO project_commitments (id, uuid, az_resource_id, amount, created_at, creator_uuid, creator_name, confirm_by, duration, expires_at, state, creation_context_json) VALUES (8, '00000000-0000-0000-0000-000000000008', 7, 300, UNIX(2), 'dummy', 'dummy', UNIX(345600), '10 days', UNIX(1209600), 'planned', '{}'::jsonb);

-- day 5: test commitments that cannot be confirmed until the previous commitment expires (ID=9 is confirmed, and then ID=10 cannot be confirmed until ID=9 expires because ID=9 blocks absolutely all available capacity in that resource and AZ)
INSERT INTO project_commitments (id, uuid, az_resource_id, amount, created_at, creator_uuid, creator_name, confirm_by, duration, expires_at, state, creation_context_json) VALUES (9, '00000000-0000-0000-0000-000000000009',  10, 22, UNIX(1), 'dummy', 'dummy', UNIX(432000), '1 hour', UNIX(435600),  'planned', '{}'::jsonb);
INSERT INTO project_commitments (id, uuid, az_resource_id, amount, created_at, creator_uuid, creator_name, confirm_by, duration, expires_at, state, creation_context_json) VALUES (10, '00000000-0000-0000-0000-000000000010', 16, 2, UNIX(2), 'dummy', 'dummy', UNIX(432000), '10 days', UNIX(1296000), 'planned', '{}'::jsonb);
