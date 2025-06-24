INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (1, 1, 'any', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (2, 1, 'az-one', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (3, 1, 'az-two', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (4, 1, 'unknown', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (5, 2, 'any', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (6, 2, 'az-one', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (7, 2, 'az-two', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (8, 2, 'unknown', 0);

INSERT INTO cluster_rates (id, service_id, name, liquid_version, topology, has_usage) VALUES (1, 1, 'firstrate', 1, 'flat', TRUE);
INSERT INTO cluster_rates (id, service_id, name, liquid_version, unit, topology, has_usage) VALUES (2, 1, 'secondrate', 1, 'KiB', 'flat', TRUE);
INSERT INTO cluster_rates (id, service_id, name) VALUES (3, 1, 'otherrate');
INSERT INTO cluster_rates (id, service_id, name) VALUES (4, 1, 'anotherrate');

INSERT INTO cluster_resources (id, service_id, name, liquid_version, unit, topology, has_capacity, needs_resource_demand, has_quota) VALUES (1, 1, 'capacity', 1, 'B', 'az-aware', TRUE, TRUE, TRUE);
INSERT INTO cluster_resources (id, service_id, name, liquid_version, topology, has_quota) VALUES (2, 1, 'things', 1, 'az-aware', TRUE);

INSERT INTO cluster_services (id, type, next_scrape_at, liquid_version, usage_metric_families_json) VALUES (1, 'unittest', 0, 1, '{"limes_unittest_capacity_usage":{"type":"gauge","help":"","labelKeys":null},"limes_unittest_things_usage":{"type":"gauge","help":"","labelKeys":null}}');

INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');

INSERT INTO project_rates_v2 (id, project_id, rate_id, rate_limit, window_ns, usage_as_bigint) VALUES (1, 2, 3, 10, 1000000000, '');
INSERT INTO project_rates_v2 (id, project_id, rate_id, rate_limit, window_ns, usage_as_bigint) VALUES (2, 1, 4, 42, 120000000000, '');

INSERT INTO project_services_v2 (id, project_id, service_id, stale, next_scrape_at) VALUES (1, 1, 1, TRUE, 0);
INSERT INTO project_services_v2 (id, project_id, service_id, stale, next_scrape_at) VALUES (2, 2, 1, TRUE, 0);

INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin');
