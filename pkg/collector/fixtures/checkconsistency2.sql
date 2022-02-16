INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (1, 'west', 'unshared', 0);
INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (4, 'west', 'shared', 1);

INSERT INTO domain_resources (service_id, name, quota) VALUES (2, 'capacity', 100);
INSERT INTO domain_resources (service_id, name, quota) VALUES (6, 'capacity', 10);

INSERT INTO domain_services (id, domain_id, type) VALUES (2, 1, 'shared');
INSERT INTO domain_services (id, domain_id, type) VALUES (4, 2, 'shared');
INSERT INTO domain_services (id, domain_id, type) VALUES (6, 1, 'unshared');
INSERT INTO domain_services (id, domain_id, type) VALUES (7, 2, 'unshared');

INSERT INTO domains (id, cluster_id, name, uuid) VALUES (1, 'west', 'germany', 'uuid-for-germany');
INSERT INTO domains (id, cluster_id, name, uuid) VALUES (2, 'west', 'france', 'uuid-for-france');

INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (1, 'capacity', 20, 0, 0, '', 0, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (9, 'capacity', 10, 0, 0, '', 10, NULL);

INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state, serialized_metrics, checked_at, scrape_error_message, rates_checked_at, rates_scrape_error_message) VALUES (1, 1, 'unshared', NULL, TRUE, 0, NULL, FALSE, 0, '', '', NULL, '', NULL, '');
INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state, serialized_metrics, checked_at, scrape_error_message, rates_checked_at, rates_scrape_error_message) VALUES (10, 3, 'shared', NULL, FALSE, 0, NULL, FALSE, 0, '', '', NULL, '', NULL, '');
INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state, serialized_metrics, checked_at, scrape_error_message, rates_checked_at, rates_scrape_error_message) VALUES (3, 2, 'unshared', NULL, FALSE, 0, NULL, FALSE, 0, '', '', NULL, '', NULL, '');
INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state, serialized_metrics, checked_at, scrape_error_message, rates_checked_at, rates_scrape_error_message) VALUES (5, 3, 'unshared', NULL, FALSE, 0, NULL, FALSE, 0, '', '', NULL, '', NULL, '');
INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state, serialized_metrics, checked_at, scrape_error_message, rates_checked_at, rates_scrape_error_message) VALUES (8, 1, 'shared', NULL, FALSE, 0, NULL, FALSE, 0, '', '', NULL, '', NULL, '');
INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state, serialized_metrics, checked_at, scrape_error_message, rates_checked_at, rates_scrape_error_message) VALUES (9, 2, 'shared', NULL, FALSE, 0, NULL, FALSE, 0, '', '', NULL, '', NULL, '');

INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany', FALSE);
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin', FALSE);
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (3, 2, 'paris', 'uuid-for-paris', 'uuid-for-france', FALSE);
