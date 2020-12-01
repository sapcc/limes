INSERT INTO domain_services (id, domain_id, type) VALUES (1, 1, 'unittest');

INSERT INTO domains (id, cluster_id, name, uuid) VALUES (1, 'west', 'germany', 'uuid-for-germany');

INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (1, 'capacity', 20, 0, 20, '', 20, 0);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (1, 'capacity_portion', NULL, 0, NULL, '', NULL, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (1, 'things', 13, 5, 13, '[{"index":0},{"index":1},{"index":2},{"index":3},{"index":4}]', 13, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (2, 'capacity', 20, 0, 24, '', 24, 0);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (2, 'capacity_portion', NULL, 0, NULL, '', NULL, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (2, 'things', 13, 5, 15, '[{"index":0},{"index":1},{"index":2},{"index":3},{"index":4}]', 15, NULL);

INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state) VALUES (1, 1, 'unittest', 14, FALSE, 1, NULL, FALSE, 0, '');
INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state) VALUES (2, 2, 'unittest', 16, FALSE, 1, NULL, FALSE, 0, '');

INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany', FALSE);
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin', TRUE);
