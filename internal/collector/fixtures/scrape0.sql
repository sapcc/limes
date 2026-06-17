INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (1, 1, 'any', 0, 'unittest/capacity/any');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (10, 2, 'unknown', 0, 'unittest/things/unknown');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (2, 1, 'az-one', 0, 'unittest/capacity/az-one');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (3, 1, 'az-two', 0, 'unittest/capacity/az-two');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (4, 1, 'total', 0, 'unittest/capacity/total');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (5, 1, 'unknown', 0, 'unittest/capacity/unknown');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (6, 2, 'any', 0, 'unittest/things/any');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (7, 2, 'az-one', 0, 'unittest/things/az-one');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (8, 2, 'az-two', 0, 'unittest/things/az-two');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (9, 2, 'total', 0, 'unittest/things/total');

INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');

INSERT INTO project_rates (id, project_id, rate_id, rate_limit, window_ns, usage_as_bigint) VALUES (1, 2, 2, 10, 1000000000, '');
INSERT INTO project_rates (id, project_id, rate_id, rate_limit, window_ns, usage_as_bigint) VALUES (2, 1, 5, 42, 120000000000, '');

INSERT INTO project_services (id, project_id, service_id, stale, next_scrape_at) VALUES (1, 1, 1, TRUE, 0);
INSERT INTO project_services (id, project_id, service_id, stale, next_scrape_at) VALUES (2, 2, 1, TRUE, 0);

INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin');

INSERT INTO rates (id, service_id, name, liquid_version, unit, topology, has_usage, path, display_name) VALUES (1, 1, 'firstrate', 1, 'piece', 'flat', TRUE, 'unittest/firstrate', 'First Rate');
INSERT INTO rates (id, service_id, name, liquid_version, unit, topology, path, display_name) VALUES (2, 1, 'rateWithClusterLimit', 1, 'piece', 'flat', 'unittest/rateWithClusterLimit', 'Cluster-Limited Rate');
INSERT INTO rates (id, service_id, name, liquid_version, unit, topology, path, display_name) VALUES (3, 1, 'rateWithProjectLimit', 1, 'piece', 'flat', 'unittest/rateWithProjectLimit', 'Project-Limited Rate');
INSERT INTO rates (id, service_id, name, liquid_version, unit, topology, has_usage, path, display_name) VALUES (4, 1, 'secondrate', 1, 'KiB', 'flat', TRUE, 'unittest/secondrate', 'Second Rate');
INSERT INTO rates (id, service_id, name, liquid_version, unit, topology, path, display_name) VALUES (5, 1, 'xAnotherRate', 1, 'piece', 'flat', 'unittest/xAnotherRate', 'X Another Rate');

INSERT INTO resources (id, service_id, name, liquid_version, unit, topology, has_capacity, needs_resource_demand, has_quota, path, display_name) VALUES (1, 1, 'capacity', 1, 'B', 'az-aware', TRUE, TRUE, TRUE, 'unittest/capacity', 'Capacity');
INSERT INTO resources (id, service_id, name, liquid_version, unit, topology, has_quota, path, display_name) VALUES (2, 1, 'things', 1, 'piece', 'az-aware', TRUE, 'unittest/things', 'Things');

INSERT INTO services (id, type, next_scrape_at, liquid_version, usage_metric_families_json, display_name) VALUES (1, 'unittest', 0, 1, '{"limes_unittest_capacity_usage":{"type":"gauge","help":"","labelKeys":null},"limes_unittest_things_usage":{"type":"gauge","help":"","labelKeys":null}}', 'Unit Test');
