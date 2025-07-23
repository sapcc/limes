CREATE OR REPLACE FUNCTION UNIXUTC(i integer) RETURNS timestamp AS $$ SELECT TO_TIMESTAMP(i) AT TIME ZONE 'Etc/UTC' $$ LANGUAGE SQL;
CREATE OR REPLACE FUNCTION UNIX(i integer) RETURNS timestamp AS $$ SELECT TO_TIMESTAMP(i) AT LOCAL $$ LANGUAGE SQL;

INSERT INTO cluster_services (id, type, scraped_at, next_scrape_at, liquid_version) VALUES (1, 'unshared', UNIXUTC(1000), UNIXUTC(2000), 1);
INSERT INTO cluster_services (id, type, scraped_at, next_scrape_at, liquid_version) VALUES (2, 'shared', UNIXUTC(1100), UNIXUTC(2100), 1);

INSERT INTO cluster_rates (id, service_id, name, liquid_version, topology, has_usage) VALUES (1, 1, 'service/unshared/instances:create', 1, 'flat', TRUE);
INSERT INTO cluster_rates (id, service_id, name, liquid_version, topology, has_usage) VALUES (2, 1, 'service/unshared/instances:delete', 1, 'flat', TRUE);
INSERT INTO cluster_rates (id, service_id, name, liquid_version, topology, has_usage) VALUES (3, 1, 'service/unshared/instances:update', 1, 'flat', TRUE);
INSERT INTO cluster_rates (id, service_id, name, liquid_version, topology, has_usage) VALUES (4, 2, 'service/shared/objects:create', 1, 'flat', TRUE);
INSERT INTO cluster_rates (id, service_id, name, liquid_version, topology, has_usage, unit) VALUES (5, 2, 'service/shared/objects:delete', 1, 'flat', TRUE, 'MiB');
INSERT INTO cluster_rates (id, service_id, name, liquid_version, topology, has_usage) VALUES (6, 2, 'service/shared/objects:update', 1, 'flat', TRUE);
INSERT INTO cluster_rates (id, service_id, name, liquid_version, topology, has_usage, unit) VALUES (7, 2, 'service/shared/objects:unlimited', 1, 'flat', TRUE, 'KiB');
INSERT INTO cluster_rates (id, service_id, name, liquid_version, topology, has_usage) VALUES (8, 2, 'service/shared/objects:read/list', 1, 'flat', TRUE);

-- all services have the resources "things" and "capacity"
INSERT INTO cluster_resources (id, service_id, name, liquid_version, topology, has_quota) VALUES (1, 1, 'things', 1, 'flat', TRUE);
INSERT INTO cluster_resources (id, service_id, name, liquid_version, unit, topology, has_capacity, needs_resource_demand, has_quota) VALUES (2, 1, 'capacity', 1, 'B', 'az-aware', TRUE, TRUE, TRUE);
INSERT INTO cluster_resources (id, service_id, name, liquid_version, topology, has_quota) VALUES (3, 2, 'things', 1, 'flat', TRUE);
INSERT INTO cluster_resources (id, service_id, name, liquid_version, unit, topology, has_capacity, needs_resource_demand, has_quota) VALUES (4, 2, 'capacity', 1, 'B', 'az-aware', TRUE, TRUE, TRUE);

-- "capacity" is modeled as AZ-aware, "things" is not
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity) VALUES (1, 1, 'any', 139, 45, '[{"smaller_half":46},{"larger_half":93}]', 139);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (2, 2, 'any', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (3, 2, 'az-one', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (4, 2, 'az-two', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity) VALUES (5, 3, 'any', 246, 158, '[{"smaller_half":82},{"larger_half":164}]', 246);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (6, 4, 'any', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity) VALUES (7, 4, 'az-one', 90, 12, '', 90);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities, last_nonzero_raw_capacity) VALUES (8, 4, 'az-two', 95, 15, '', 95);

-- two domains
INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');
INSERT INTO domains (id, name, uuid) VALUES (2, 'france',  'uuid-for-france');

-- "germany" has two projects, the other domains have one each (Dresden is a child project of Berlin in order to check
-- correct rendering of the parent_uuid field)
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (3, 2, 'paris', 'uuid-for-paris', 'uuid-for-france');

-- project_services is fully populated (as ensured by the collector's consistency check)
INSERT INTO project_services (id, project_id, service_id, scraped_at, checked_at) VALUES (1, 1, 1, UNIX(11), UNIX(11));
INSERT INTO project_services (id, project_id, service_id, scraped_at, checked_at) VALUES (2, 1, 2,   UNIX(22), UNIX(22));
INSERT INTO project_services (id, project_id, service_id, scraped_at, checked_at) VALUES (3, 2, 1, UNIX(33), UNIX(33));
INSERT INTO project_services (id, project_id, service_id, scraped_at, checked_at) VALUES (4, 2, 2,   UNIX(44), UNIX(44));
INSERT INTO project_services (id, project_id, service_id, scraped_at, checked_at) VALUES (5, 3, 1, UNIX(55), UNIX(55));
INSERT INTO project_services (id, project_id, service_id, scraped_at, checked_at) VALUES (6, 3, 2,   UNIX(66), UNIX(66));

-- project_resources contains some pathological cases
-- berlin (also used for test cases concerning subresources)
INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (1,  1, 1,   10, 10);
INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (2,  1, 2, 10, 10);
INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (3,  1, 3,   10, 10);
INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (4,  1, 4, 10, 10);
-- dresden (backend quota for shared/capacity mismatches approved quota and exceeds domain quota)
INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (5, 2,  1,   10, 10);
INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (6, 2,  2, 10, 10);
INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (7, 2, 3,   10, 10);
INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (8, 2, 4, 10, 100);
-- paris (infinite backend quota for unshared/things; non-null physical_usage for */capacity, all other project resources should report physical_usage = usage in aggregations)
INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (9, 3, 1,   10, -1);
INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (10, 3, 2, 10, 10);
INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (11, 3, 3,   10, 10);
INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota, max_quota_from_outside_admin) VALUES (12, 3, 4, 10, 10, 200);

-- "capacity" is modeled as AZ-aware, "things" is not
-- NOTE: AZ-aware resources also have an entry for AZ "any" with 0 usage
--       (this is consistent with what Scrape does, and reporting should ignore those entries)
-- NOTE: the projects in domain "germany" have AZ-aware quota to test the new report style, the one in domain "france" does not to test the old report style
--       (TODO: migrate the latter to AZ-aware quota once we remove HQD)
--
-- berlin (also used for test cases concerning subresources)
INSERT INTO project_az_resources (id, project_id, az_resource_id, quota, usage, physical_usage, subresources) VALUES (1, 1, 1, 10,   2, NULL, '[{"id":"firstthing","value":23},{"id":"secondthing","value":42}]');
INSERT INTO project_az_resources (id, project_id, az_resource_id, quota, usage, physical_usage, subresources) VALUES (2, 1, 2, 0,    0, NULL, '');
INSERT INTO project_az_resources (id, project_id, az_resource_id, quota, usage, physical_usage, subresources) VALUES (3, 1, 3, 5,    1, NULL, '');
INSERT INTO project_az_resources (id, project_id, az_resource_id, quota, usage, physical_usage, subresources) VALUES (4, 1, 4, 5,    1, NULL, '');
INSERT INTO project_az_resources (id, project_id, az_resource_id, quota, usage, physical_usage, subresources) VALUES (5, 1, 5, 10,   2, NULL, '[{"id":"thirdthing","value":5},{"id":"fourththing","value":123}]');
INSERT INTO project_az_resources (id, project_id, az_resource_id, quota, usage, physical_usage, subresources) VALUES (6, 1, 6, 0,    0, NULL, '');
INSERT INTO project_az_resources (id, project_id, az_resource_id, quota, usage, physical_usage, subresources) VALUES (7, 1, 7, 5,    1, NULL, '');
INSERT INTO project_az_resources (id, project_id, az_resource_id, quota, usage, physical_usage, subresources) VALUES (8, 1, 8, 5,    1, NULL, '');
-- dresden
INSERT INTO project_az_resources (id, project_id, az_resource_id, quota, usage, physical_usage, subresources) VALUES (9, 2, 1, 10,   2, NULL, '');
INSERT INTO project_az_resources (id, project_id, az_resource_id, quota, usage, physical_usage, subresources) VALUES (10, 2,  2, 4,    0, NULL, '');
INSERT INTO project_az_resources (id, project_id, az_resource_id, quota, usage, physical_usage, subresources) VALUES (11, 2,  3, 3,    1, NULL, '');
INSERT INTO project_az_resources (id, project_id, az_resource_id, quota, usage, physical_usage, subresources) VALUES (12, 2,  4, 3,    1, NULL, '');
INSERT INTO project_az_resources (id, project_id, az_resource_id, quota, usage, physical_usage, subresources) VALUES (13, 2,  5, 10,   2, NULL, '');
INSERT INTO project_az_resources (id, project_id, az_resource_id, quota, usage, physical_usage, subresources) VALUES (14, 2,  6, 4,    0, NULL, '');
INSERT INTO project_az_resources (id, project_id, az_resource_id, quota, usage, physical_usage, subresources) VALUES (15, 2,  7, 3,    1, NULL, '');
INSERT INTO project_az_resources (id, project_id, az_resource_id, quota, usage, physical_usage, subresources) VALUES (16, 2,  8, 3,    1, NULL, '');
-- paris (non-null physical_usage for */capacity, all other project resources should report physical_usage = usage in aggregations)
INSERT INTO project_az_resources (id, project_id, az_resource_id, quota, usage, physical_usage, subresources) VALUES (17, 3, 1, NULL, 2, NULL, '');
INSERT INTO project_az_resources (id, project_id, az_resource_id, quota, usage, physical_usage, subresources) VALUES (18, 3, 2, NULL, 0, NULL, '');
INSERT INTO project_az_resources (id, project_id, az_resource_id, quota, usage, physical_usage, subresources) VALUES (19, 3, 3, NULL, 1, 0, '');
INSERT INTO project_az_resources (id, project_id, az_resource_id, quota, usage, physical_usage, subresources) VALUES (20, 3, 4, NULL, 1, 1, '');
INSERT INTO project_az_resources (id, project_id, az_resource_id, quota, usage, physical_usage, subresources) VALUES (21, 3, 5, NULL, 2, NULL, '');
INSERT INTO project_az_resources (id, project_id, az_resource_id, quota, usage, physical_usage, subresources) VALUES (22, 3, 6, NULL, 0, NULL, '');
INSERT INTO project_az_resources (id, project_id, az_resource_id, quota, usage, physical_usage, subresources) VALUES (23, 3, 7, NULL, 1, 0, '');
INSERT INTO project_az_resources (id, project_id, az_resource_id, quota, usage, physical_usage, subresources) VALUES (24, 3, 8, NULL, 1, 1, '');

-- project_commitments has several entries for project dresden
-- on "unshared/capacity": regular active commitments with different durations
INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, amount, duration, created_at, creator_uuid, creator_name, confirm_by, confirmed_at, expires_at, state, creation_context_json) VALUES (1, '00000000-0000-0000-0000-000000000001', 2, 3, 1,   '2 years',    UNIX(1), 'uuid-for-alice', 'alice@Default', UNIX(1),       UNIX(1), UNIX(100000001), 'active',  '{}'::jsonb);
INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, amount, duration, created_at, creator_uuid, creator_name, confirm_by, confirmed_at, expires_at, state, creation_context_json) VALUES (2, '00000000-0000-0000-0000-000000000002', 2, 3, 1,   '1 year',     UNIX(2), 'uuid-for-alice', 'alice@Default', UNIX(2),       UNIX(2), UNIX(100000002), 'active',  '{}'::jsonb);
INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, amount, duration, created_at, creator_uuid, creator_name, confirm_by, confirmed_at, expires_at, state, creation_context_json) VALUES (3, '00000000-0000-0000-0000-000000000003', 2, 3, 1,   '1 year',     UNIX(3), 'uuid-for-alice', 'alice@Default', UNIX(3),       UNIX(3), UNIX(100000003), 'active',  '{}'::jsonb);
INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, amount, duration, created_at, creator_uuid, creator_name, confirm_by, confirmed_at, expires_at, state, creation_context_json) VALUES (4, '00000000-0000-0000-0000-000000000004', 2, 4, 2,   '1 year',     UNIX(4), 'uuid-for-alice', 'alice@Default', UNIX(4),       UNIX(4), UNIX(100000004), 'active',  '{}'::jsonb);
-- on "unshared/capacity": unconfirmed commitments should be reported as "pending"
INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, amount, duration, created_at, creator_uuid, creator_name, confirm_by, confirmed_at, expires_at, state, creation_context_json) VALUES (5, '00000000-0000-0000-0000-000000000005', 2, 4, 100, '2 years',    UNIX(5), 'uuid-for-alice', 'alice@Default', UNIX(5),       NULL,    UNIX(100000005), 'pending', '{}'::jsonb);
-- on "unshared/capacity": expired commitments should not be reported (NOTE: the test's clock stands at UNIX timestamp 3600)
INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, amount, duration, created_at, creator_uuid, creator_name, confirm_by, confirmed_at, expires_at, state, creation_context_json) VALUES (6, '00000000-0000-0000-0000-000000000006', 2, 3, 5,   '10 minutes', UNIX(6), 'uuid-for-alice', 'alice@Default', UNIX(6),       UNIX(6), UNIX(606),       'expired', '{}'::jsonb);
-- on "shared/capacity": only an unconfirmed commitment that should be reported as "planned", this tests that the "committed" structure is absent in the JSON for that resource
INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, amount, duration, created_at, creator_uuid, creator_name, confirm_by, confirmed_at, expires_at, state, creation_context_json) VALUES (7, '00000000-0000-0000-0000-000000000007', 2, 7, 100, '2 years',    UNIX(7), 'uuid-for-alice', 'alice@Default', UNIX(1000007), NULL,    UNIX(100000007), 'planned', '{}'::jsonb);
-- on "unshared/things": an active commitment on AZ "any"
INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, amount, duration, created_at, creator_uuid, creator_name, confirm_by, confirmed_at, expires_at, state, creation_context_json) VALUES (8, '00000000-0000-0000-0000-000000000008', 2, 1, 1,   '2 years',    UNIX(8), 'uuid-for-alice', 'alice@Default', UNIX(8),       UNIX(8), UNIX(100000008), 'active',  '{}'::jsonb);

-- project_rates also has multiple different setups to test different cases
-- berlin has custom rate limits
INSERT INTO project_rates (id, project_id, rate_id, rate_limit, window_ns, usage_as_bigint) VALUES (1, 1, 1, 5, 60000000000, '');
INSERT INTO project_rates (id, project_id, rate_id, rate_limit, window_ns, usage_as_bigint) VALUES (2, 1, 2, 2, 60000000000, '12345');
INSERT INTO project_rates (id, project_id, rate_id, rate_limit, window_ns, usage_as_bigint) VALUES (3, 1, 3, 2, 60000000000, '');
INSERT INTO project_rates (id, project_id, rate_id, rate_limit, window_ns, usage_as_bigint) VALUES (4, 1, 4, 5, 60000000000, '');
INSERT INTO project_rates (id, project_id, rate_id, rate_limit, window_ns, usage_as_bigint) VALUES (5, 1, 5, 2, 60000000000, '23456');
INSERT INTO project_rates (id, project_id, rate_id, rate_limit, window_ns, usage_as_bigint) VALUES (6, 1, 6, 2, 60000000000, '');
-- dresden only has usage values, and it also shows usage for a rate that does not have rate limits
-- also, dresden has some zero-valued usage values, which is different from empty string (empty string means "usage unknown", 0 means "no usage yet")
INSERT INTO project_rates (id, project_id, rate_id, rate_limit, window_ns, usage_as_bigint) VALUES (7, 2, 2, NULL, NULL, '0');
INSERT INTO project_rates (id, project_id, rate_id, rate_limit, window_ns, usage_as_bigint) VALUES (8, 2, 5, NULL, NULL, '0');
INSERT INTO project_rates (id, project_id, rate_id, rate_limit, window_ns, usage_as_bigint) VALUES (9, 2, 7, NULL, NULL, '1048576');
-- not pictured: paris has no records at all, so the API will only display the default rate limits

-- insert some bullshit data that should be filtered out by the internal/reports/ logic
-- (cluster "north", service "weird", resource "items" and rate "frobnicate" are not configured)
INSERT INTO cluster_services (id, type, liquid_version) VALUES (101, 'weird', 1);
INSERT INTO cluster_resources (id, service_id, name, liquid_version) VALUES (101, 101, 'things', 1);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, subcapacities) VALUES (101, 101, 'any', 2, 1, '');
