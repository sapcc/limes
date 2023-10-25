CREATE OR REPLACE FUNCTION unix(i integer) RETURNS timestamp AS $$ SELECT TO_TIMESTAMP(i) AT TIME ZONE 'Etc/UTC' $$ LANGUAGE SQL;

-- two capacitors matching the two services that have capacity values
INSERT INTO cluster_capacitors (capacitor_id, scraped_at, next_scrape_at) VALUES ('scans-unshared', UNIX(1000), UNIX(2000));
INSERT INTO cluster_capacitors (capacitor_id, scraped_at, next_scrape_at) VALUES ('scans-shared',   UNIX(1100), UNIX(2100));

-- three services
INSERT INTO cluster_services (id, type) VALUES (1, 'unshared');
INSERT INTO cluster_services (id, type) VALUES (2, 'shared');

-- all services have the resources "things" and "capacity"
INSERT INTO cluster_resources (id, service_id, name, capacitor_id) VALUES (1, 1, 'things', 'scans-unshared');
INSERT INTO cluster_resources (id, service_id, name, capacitor_id) VALUES (2, 2, 'things', 'scans-shared');
INSERT INTO cluster_resources (id, service_id, name, capacitor_id) VALUES (3, 2, 'capacity', 'scans-shared');

-- "capacity" is modeled as AZ-aware, "things" is not
INSERT INTO cluster_az_resources (resource_id, az, raw_capacity, usage, subcapacities) VALUES (1, 'any', 139, 45, '[{"smaller_half":46},{"larger_half":93}]');
INSERT INTO cluster_az_resources (resource_id, az, raw_capacity, usage, subcapacities) VALUES (2, 'any', 246, 158, '[{"smaller_half":82},{"larger_half":164}]');
INSERT INTO cluster_az_resources (resource_id, az, raw_capacity, usage, subcapacities) VALUES (3, 'az-one', 90, 12, '');
INSERT INTO cluster_az_resources (resource_id, az, raw_capacity, usage, subcapacities) VALUES (3, 'az-two', 95, 15, '');

-- two domains
INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');
INSERT INTO domains (id, name, uuid) VALUES (2, 'france',  'uuid-for-france');

-- domain_services is fully populated (as ensured by the collector's consistency check)
INSERT INTO domain_services (id, domain_id, type) VALUES (1, 1, 'unshared');
INSERT INTO domain_services (id, domain_id, type) VALUES (2, 1, 'shared');
INSERT INTO domain_services (id, domain_id, type) VALUES (3, 2, 'unshared');
INSERT INTO domain_services (id, domain_id, type) VALUES (4, 2, 'shared');

-- domain_resources has some holes where no domain quotas have been set yet (and we don't have anything for "capacity_portion" since it's NoQuota)
INSERT INTO domain_resources (id, service_id, name, quota) VALUES (1, 1, 'things',   50);
INSERT INTO domain_resources (id, service_id, name, quota) VALUES (2, 1, 'capacity', 45);
INSERT INTO domain_resources (id, service_id, name, quota) VALUES (3, 2, 'things',   30);
INSERT INTO domain_resources (id, service_id, name, quota) VALUES (4, 2, 'capacity', 25);
INSERT INTO domain_resources (id, service_id, name, quota) VALUES (5, 3, 'things',   20);
INSERT INTO domain_resources (id, service_id, name, quota) VALUES (6, 3, 'capacity', 55);

-- "germany" has two projects, the other domains have one each (Dresden is a child project of Berlin in order to check
-- correct rendering of the parent_uuid field)
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany', FALSE);
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin', FALSE);
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (3, 2, 'paris', 'uuid-for-paris', 'uuid-for-france', FALSE);

-- project_services is fully populated (as ensured by the collector's consistency check)
INSERT INTO project_services (id, project_id, type, scraped_at, rates_scraped_at, checked_at, rates_checked_at) VALUES (1, 1, 'unshared', UNIX(11), UNIX(12), UNIX(11), UNIX(12));
INSERT INTO project_services (id, project_id, type, scraped_at, rates_scraped_at, checked_at, rates_checked_at) VALUES (2, 1, 'shared',   UNIX(22), UNIX(23), UNIX(22), UNIX(23));
INSERT INTO project_services (id, project_id, type, scraped_at, rates_scraped_at, checked_at, rates_checked_at) VALUES (3, 2, 'unshared', UNIX(33), UNIX(34), UNIX(33), UNIX(34));
INSERT INTO project_services (id, project_id, type, scraped_at, rates_scraped_at, checked_at, rates_checked_at) VALUES (4, 2, 'shared',   UNIX(44), UNIX(45), UNIX(44), UNIX(45));
INSERT INTO project_services (id, project_id, type, scraped_at, rates_scraped_at, checked_at, rates_checked_at) VALUES (5, 3, 'unshared', UNIX(55), NULL, UNIX(55), NULL);
INSERT INTO project_services (id, project_id, type, scraped_at, rates_scraped_at, checked_at, rates_checked_at) VALUES (6, 3, 'shared',   UNIX(66), NULL, UNIX(66), NULL);

-- project_resources contains some pathological cases
-- berlin (also used for test cases concerning subresources)
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (1,  1, 'things',   10, 10, 10);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (2,  1, 'capacity', 10, 10, 10);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (3,  1, 'capacity_portion', NULL, NULL, NULL);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (4,  2, 'things',   10, 10, 10);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (5,  2, 'capacity', 10, 10, 10);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (6,  2, 'capacity_portion', NULL, NULL, NULL);
-- dresden (backend quota for shared/capacity mismatches approved quota and exceeds domain quota)
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (7,  3, 'things',   10, 10, 10);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (8,  3, 'capacity', 10, 10, 10);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (9,  3, 'capacity_portion', NULL, NULL, NULL);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (10, 4, 'things',   10, 10, 10);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (11, 4, 'capacity', 10, 100, 10);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (12, 4, 'capacity_portion', NULL, NULL, NULL);
-- paris (infinite backend quota for unshared/things; non-null physical_usage for */capacity, all other project resources should report physical_usage = usage in aggregations)
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (13, 5, 'things',   10, -1, 10);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (14, 5, 'capacity', 10, 10, 10);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (15, 5, 'capacity_portion', NULL, NULL, NULL);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (16, 6, 'things',   10, 10, 10);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (17, 6, 'capacity', 10, 10, 10);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (18, 6, 'capacity_portion', NULL, NULL, NULL);

-- "capacity" is modeled as AZ-aware, "things" is not
-- berlin (also used for test cases concerning subresources)
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (1,  'any',    NULL, 2, NULL, '[{"id":"firstthing","value":23},{"id":"secondthing","value":42}]');
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (2,  'az-one', NULL, 1, NULL, '');
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (2,  'az-two', NULL, 1, NULL, '');
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (3,  'az-one', NULL, 1, NULL, '');
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (3,  'az-two', NULL, 0, NULL, '');
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (4,  'any',    NULL, 2, NULL, '[{"id":"thirdthing","value":5},{"id":"fourththing","value":123}]');
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (5,  'az-one', NULL, 1, NULL, '');
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (5,  'az-two', NULL, 1, NULL, '');
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (6,  'az-one', NULL, 1, NULL, '');
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (6,  'az-two', NULL, 0, NULL, '');
-- dresden
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (7,  'any',    NULL, 2, NULL, '');
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (8,  'az-one', NULL, 1, NULL, '');
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (8,  'az-two', NULL, 1, NULL, '');
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (9,  'az-one', NULL, 1, NULL, '');
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (9,  'az-two', NULL, 0, NULL, '');
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (10, 'any',    NULL, 2, NULL, '');
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (11, 'az-one', NULL, 1, NULL, '');
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (11, 'az-two', NULL, 1, NULL, '');
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (12, 'az-one', NULL, 1, NULL, '');
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (12, 'az-two', NULL, 0, NULL, '');
-- paris (non-null physical_usage for */capacity, all other project resources should report physical_usage = usage in aggregations)
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (13, 'any',    NULL, 2, NULL, '');
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (14, 'az-one', NULL, 1, 0, '');
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (14, 'az-two', NULL, 1, 1, '');
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (15, 'az-one', NULL, 1, NULL, '');
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (15, 'az-two', NULL, 0, NULL, '');
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (16, 'any',    NULL, 2, NULL, '');
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (17, 'az-one', NULL, 1, 0, '');
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (17, 'az-two', NULL, 1, 1, '');
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (18, 'az-one', NULL, 1, NULL, '');
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (18, 'az-two', NULL, 0, NULL, '');

-- project_rates also has multiple different setups to test different cases
-- berlin has custom rate limits
INSERT INTO project_rates (service_id, name, rate_limit, window_ns, usage_as_bigint) VALUES (1, 'service/unshared/instances:create', 5, 60000000000, '');
INSERT INTO project_rates (service_id, name, rate_limit, window_ns, usage_as_bigint) VALUES (1, 'service/unshared/instances:delete', 2, 60000000000, '12345');
INSERT INTO project_rates (service_id, name, rate_limit, window_ns, usage_as_bigint) VALUES (1, 'service/unshared/instances:update', 2, 60000000000, '');
INSERT INTO project_rates (service_id, name, rate_limit, window_ns, usage_as_bigint) VALUES (2, 'service/shared/objects:create', 5, 60000000000, '');
INSERT INTO project_rates (service_id, name, rate_limit, window_ns, usage_as_bigint) VALUES (2, 'service/shared/objects:delete', 2, 60000000000, '23456');
INSERT INTO project_rates (service_id, name, rate_limit, window_ns, usage_as_bigint) VALUES (2, 'service/shared/objects:update', 2, 60000000000, '');
-- dresden only has usage values, and it also shows usage for a rate that does not have rate limits
-- also, dresden has some zero-valued usage values, which is different from empty string (empty string means "usage unknown", 0 means "no usage yet")
INSERT INTO project_rates (service_id, name, rate_limit, window_ns, usage_as_bigint) VALUES (3, 'service/unshared/instances:delete', NULL, NULL, '0');
INSERT INTO project_rates (service_id, name, rate_limit, window_ns, usage_as_bigint) VALUES (4, 'service/shared/objects:delete', NULL, NULL, '0');
INSERT INTO project_rates (service_id, name, rate_limit, window_ns, usage_as_bigint) VALUES (4, 'service/shared/objects:unlimited', NULL, NULL, '1048576');
-- not pictured: paris has no records at all, so the API will only display the default rate limits

-- insert some bullshit data that should be filtered out by the internal/reports/ logic
-- (cluster "north", service "weird", resource "items" and rate "frobnicate" are not configured)
INSERT INTO cluster_services (id, type) VALUES (101, 'weird');
INSERT INTO cluster_resources (id, service_id, name, capacitor_id) VALUES (101, 101, 'things', 'scans-shared');
INSERT INTO cluster_az_resources (resource_id, az, raw_capacity, usage, subcapacities) VALUES (101, 'any', 2, 1, '');
INSERT INTO domain_services (id, domain_id, type) VALUES (101, 1, 'weird');
INSERT INTO domain_resources (id, service_id, name, quota) VALUES (101, 101, 'things', 1);
INSERT INTO project_services (id, project_id, type) VALUES (101, 1, 'weird');
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (101, 101, 'things', 2, 2, 2);
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (101, 'any', NULL, 1, 1, '');

INSERT INTO domain_resources (id, service_id, name, quota) VALUES (102, 1, 'items', 1);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (102, 1, 'items', 2, 2, 2);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (103, 1, 'items_portion', NULL, NULL, NULL);
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (102, 'any', NULL, 1, 1, '');
INSERT INTO project_az_resources (resource_id, az, quota, usage, physical_usage, subresources) VALUES (103, 'any', NULL, 1, NULL, '');

INSERT INTO project_rates (service_id, name, rate_limit, window_ns, usage_as_bigint) VALUES (1, 'service/unshared/instances:frobnicate', 5, 1000000000, '');
INSERT INTO project_rates (service_id, name, rate_limit, window_ns, usage_as_bigint) VALUES (2, 'service/shared/objects:frobnicate', 5, 1000000000, '');
