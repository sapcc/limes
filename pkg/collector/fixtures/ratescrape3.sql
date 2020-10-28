INSERT INTO domain_services (id, domain_id, type) VALUES (1, 1, 'unittest');

INSERT INTO domains (id, cluster_id, name, uuid) VALUES (1, 'west', 'germany', 'uuid-for-germany');

INSERT INTO project_rates (service_id, name, rate_limit, window_ns, usage_as_bigint) VALUES (1, 'firstrate', NULL, NULL, '5129');
INSERT INTO project_rates (service_id, name, rate_limit, window_ns, usage_as_bigint) VALUES (1, 'otherrate', 42, 120000000000, '');
INSERT INTO project_rates (service_id, name, rate_limit, window_ns, usage_as_bigint) VALUES (1, 'secondrate', 10, 1000000000, '1034');
INSERT INTO project_rates (service_id, name, rate_limit, window_ns, usage_as_bigint) VALUES (2, 'firstrate', NULL, NULL, '1033');
INSERT INTO project_rates (service_id, name, rate_limit, window_ns, usage_as_bigint) VALUES (2, 'secondrate', NULL, NULL, '1034');

INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state) VALUES (1, 1, 'unittest', NULL, FALSE, 0, 7, FALSE, 1, '{"firstrate":5120,"secondrate":1024}');
INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state) VALUES (2, 2, 'unittest', NULL, FALSE, 0, 9, FALSE, 1, '{"firstrate":1024,"secondrate":1024}');

INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany', FALSE);
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin', TRUE);
