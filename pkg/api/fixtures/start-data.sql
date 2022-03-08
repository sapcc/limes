CREATE OR REPLACE FUNCTION unix(i integer) RETURNS timestamp AS $$ SELECT TO_TIMESTAMP(i) AT TIME ZONE 'Etc/UTC' $$ LANGUAGE SQL;

-- two services, one shared, one unshared
INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (1, 'west', 'unshared', UNIX(1000));
INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (2, 'west', 'shared',   UNIX(1100));

-- both services have the resources "things" and "capacity"
INSERT INTO cluster_resources (service_id, name, capacity, subcapacities, capacity_per_az) VALUES (1, 'things', 139, '[{"smaller_half":46},{"larger_half":93}]', '[{"name":"az-one","capacity":69,"usage":13},{"name":"az-two","capacity":69,"usage":13}]');
INSERT INTO cluster_resources (service_id, name, capacity, subcapacities, capacity_per_az) VALUES (2, 'things', 246, '[{"smaller_half":82},{"larger_half":164}]', '');
INSERT INTO cluster_resources (service_id, name, capacity, subcapacities, capacity_per_az) VALUES (2, 'capacity', 185, '', '');

-- cluster "west" has two domains
INSERT INTO domains (id, cluster_id, name, uuid) VALUES (1, 'west', 'germany', 'uuid-for-germany');
INSERT INTO domains (id, cluster_id, name, uuid) VALUES (2, 'west', 'france',  'uuid-for-france');

-- domain_services is fully populated (as ensured by the collector's consistency check)
INSERT INTO domain_services (id, domain_id, type) VALUES (1, 1, 'unshared');
INSERT INTO domain_services (id, domain_id, type) VALUES (2, 1, 'shared');
INSERT INTO domain_services (id, domain_id, type) VALUES (3, 2, 'unshared');
INSERT INTO domain_services (id, domain_id, type) VALUES (4, 2, 'shared');

-- domain_resources has some holes where no domain quotas have been set yet (and we don't have anything for "capacity_portion" since it's NoQuota)
INSERT INTO domain_resources (service_id, name, quota) VALUES (1, 'things',   50);
INSERT INTO domain_resources (service_id, name, quota) VALUES (1, 'capacity', 45);
INSERT INTO domain_resources (service_id, name, quota) VALUES (2, 'things',   30);
INSERT INTO domain_resources (service_id, name, quota) VALUES (2, 'capacity', 25);
INSERT INTO domain_resources (service_id, name, quota) VALUES (3, 'things',   20);
INSERT INTO domain_resources (service_id, name, quota) VALUES (3, 'capacity', 55);

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
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (1, 'things',   10, 2, 10, '[{"id":"firstthing","value":23},{"id":"secondthing","value":42}]', 10, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (1, 'capacity', 10, 2, 10, '', 10, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (1, 'capacity_portion', NULL, 1, NULL, '', NULL, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (2, 'things',   10, 2, 10, '[{"id":"thirdthing","value":5},{"id":"fourththing","value":123}]', 10, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (2, 'capacity', 10, 2, 10, '', 10, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (2, 'capacity_portion', NULL, 1, NULL, '', NULL, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (2, 'external_things', 1, 0, 1, '', 10, NULL);
-- dresden (backend quota for shared/capacity mismatches approved quota and exceeds domain quota)
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (3, 'things',   10, 2, 10, '', 10, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (3, 'capacity', 10, 2, 10, '', 10, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (3, 'capacity_portion', NULL, 1, NULL, '', NULL, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (4, 'things',   10, 2, 10, '', 10, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (4, 'capacity', 10, 2, 100, '', 10, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (4, 'capacity_portion', NULL, 1, NULL, '', NULL, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (4, 'external_things', 1, 0, 1, '', 10, NULL);
-- paris (infinite backend quota for unshared/things; non-null physical_usage for */capacity, all other project resources should report physical_usage = usage in aggregations)
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (5, 'things',   10, 2, -1, '', 10, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (5, 'capacity', 10, 2, 10, '', 10, 1);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (5, 'capacity_portion', NULL, 1, NULL, '', NULL, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (6, 'things',   10, 2, 10, '', 10, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (6, 'capacity', 10, 2, 10, '', 10, 1);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (6, 'capacity_portion', NULL, 1, NULL, '', NULL, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (6, 'external_things', 1, 0, 1, '', 10, NULL);

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

-- insert some bullshit data that should be filtered out by the pkg/reports/ logic
-- (cluster "north", service "weird", resource "items" and rate "frobnicate" are not configured)
INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (101, 'north', 'unshared', UNIX(1000));
INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (102, 'north', 'shared',   UNIX(1100));
INSERT INTO cluster_resources (service_id, name, capacity) VALUES (101, 'things', 1);
INSERT INTO cluster_resources (service_id, name, capacity) VALUES (102, 'things', 1);

INSERT INTO domain_services (id, domain_id, type) VALUES (101, 1, 'weird');
INSERT INTO domain_resources (service_id, name, quota) VALUES (101, 'things', 1);
INSERT INTO project_services (id, project_id, type) VALUES (101, 1, 'weird');
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (101, 'things', 2, 1, 2, '', 2, 1);

INSERT INTO domain_resources (service_id, name, quota) VALUES (1, 'items', 1);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (1, 'items', 2, 1, 2, '', 2, 1);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (1, 'items_portion', NULL, 1, NULL, '', NULL, NULL);

INSERT INTO project_rates (service_id, name, rate_limit, window_ns, usage_as_bigint) VALUES (1, 'service/unshared/instances:frobnicate', 5, 1000000000, '');
INSERT INTO project_rates (service_id, name, rate_limit, window_ns, usage_as_bigint) VALUES (2, 'service/shared/objects:frobnicate', 5, 1000000000, '');
