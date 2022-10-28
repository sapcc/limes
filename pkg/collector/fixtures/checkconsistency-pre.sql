INSERT INTO domain_resources (service_id, name, quota) VALUES (1, 'capacity', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (1, 'capacity_portion', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (1, 'things', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (2, 'capacity', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (2, 'capacity_portion', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (2, 'things', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (3, 'capacity', 20);
INSERT INTO domain_resources (service_id, name, quota) VALUES (3, 'capacity_portion', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (3, 'things', 30);
INSERT INTO domain_resources (service_id, name, quota) VALUES (4, 'capacity', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (4, 'capacity_portion', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (4, 'things', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (5, 'capacity', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (5, 'capacity_portion', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (5, 'things', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (6, 'capacity', 10);
INSERT INTO domain_resources (service_id, name, quota) VALUES (6, 'capacity_portion', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (6, 'things', 15);

INSERT INTO domain_services (id, domain_id, type) VALUES (1, 1, 'unshared');
INSERT INTO domain_services (id, domain_id, type) VALUES (2, 1, 'shared');
INSERT INTO domain_services (id, domain_id, type) VALUES (3, 1, 'centralized');
INSERT INTO domain_services (id, domain_id, type) VALUES (4, 2, 'unshared');
INSERT INTO domain_services (id, domain_id, type) VALUES (5, 2, 'shared');
INSERT INTO domain_services (id, domain_id, type) VALUES (6, 2, 'centralized');

INSERT INTO domains (id, cluster_id, name, uuid) VALUES (1, 'west', 'germany', 'uuid-for-germany');
INSERT INTO domains (id, cluster_id, name, uuid) VALUES (2, 'west', 'france', 'uuid-for-france');

INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (3, 'capacity', 10, 0, 0, '', 10, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (3, 'things', 15, 0, 0, '', 15, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (6, 'capacity', 10, 0, 0, '', 10, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (6, 'things', 15, 0, 0, '', 15, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (9, 'capacity', 10, 0, 0, '', 10, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (9, 'things', 15, 0, 0, '', 15, NULL);

INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state, serialized_metrics, checked_at, scrape_error_message, rates_checked_at, rates_scrape_error_message) VALUES (1, 1, 'unshared', NULL, FALSE, 0, NULL, FALSE, 0, '', '', NULL, '', NULL, '');
INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state, serialized_metrics, checked_at, scrape_error_message, rates_checked_at, rates_scrape_error_message) VALUES (2, 1, 'shared', NULL, FALSE, 0, NULL, FALSE, 0, '', '', NULL, '', NULL, '');
INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state, serialized_metrics, checked_at, scrape_error_message, rates_checked_at, rates_scrape_error_message) VALUES (3, 1, 'centralized', NULL, FALSE, 0, NULL, FALSE, 0, '', '', NULL, '', NULL, '');
INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state, serialized_metrics, checked_at, scrape_error_message, rates_checked_at, rates_scrape_error_message) VALUES (4, 2, 'unshared', NULL, FALSE, 0, NULL, FALSE, 0, '', '', NULL, '', NULL, '');
INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state, serialized_metrics, checked_at, scrape_error_message, rates_checked_at, rates_scrape_error_message) VALUES (5, 2, 'shared', NULL, FALSE, 0, NULL, FALSE, 0, '', '', NULL, '', NULL, '');
INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state, serialized_metrics, checked_at, scrape_error_message, rates_checked_at, rates_scrape_error_message) VALUES (6, 2, 'centralized', NULL, FALSE, 0, NULL, FALSE, 0, '', '', NULL, '', NULL, '');
INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state, serialized_metrics, checked_at, scrape_error_message, rates_checked_at, rates_scrape_error_message) VALUES (7, 3, 'unshared', NULL, FALSE, 0, NULL, FALSE, 0, '', '', NULL, '', NULL, '');
INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state, serialized_metrics, checked_at, scrape_error_message, rates_checked_at, rates_scrape_error_message) VALUES (8, 3, 'shared', NULL, FALSE, 0, NULL, FALSE, 0, '', '', NULL, '', NULL, '');
INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state, serialized_metrics, checked_at, scrape_error_message, rates_checked_at, rates_scrape_error_message) VALUES (9, 3, 'centralized', NULL, FALSE, 0, NULL, FALSE, 0, '', '', NULL, '', NULL, '');

INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany', FALSE);
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin', FALSE);
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (3, 2, 'paris', 'uuid-for-paris', 'uuid-for-france', FALSE);
