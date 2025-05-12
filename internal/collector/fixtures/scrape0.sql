INSERT INTO cluster_resources (id, service_id, name, liquid_version, unit, topology, has_capacity, needs_resource_demand, has_quota) VALUES (1, 1, 'capacity', 1, 'B', 'az-aware', TRUE, TRUE, TRUE);
INSERT INTO cluster_resources (id, service_id, name, liquid_version, topology, has_quota) VALUES (2, 1, 'things', 1, 'az-aware', TRUE);

INSERT INTO cluster_services (id, type, next_scrape_at, liquid_version, usage_metric_families_json) VALUES (1, 'unittest', -62135596800, 1, '{"limes_unittest_capacity_usage":{"type":"gauge","help":"","labelKeys":null},"limes_unittest_things_usage":{"type":"gauge","help":"","labelKeys":null}}');

INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');

INSERT INTO project_services (id, project_id, type, stale, rates_stale, next_scrape_at, rates_next_scrape_at) VALUES (1, 1, 'unittest', TRUE, TRUE, 0, 0);
INSERT INTO project_services (id, project_id, type, stale, rates_stale, next_scrape_at, rates_next_scrape_at) VALUES (2, 2, 'unittest', TRUE, TRUE, 0, 0);

INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin');
