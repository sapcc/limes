INSERT INTO cluster_services (id, type, scraped_at) VALUES (1, 'centralized', 0);
INSERT INTO cluster_services (id, type, scraped_at) VALUES (3, 'unshared', 0);
INSERT INTO cluster_services (id, type, scraped_at) VALUES (5, 'shared', 1);

INSERT INTO domain_resources (service_id, name, quota) VALUES (1, 'capacity', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (1, 'capacity_portion', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (1, 'things', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (2, 'capacity', 100);
INSERT INTO domain_resources (service_id, name, quota) VALUES (2, 'capacity_portion', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (2, 'things', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (4, 'capacity', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (4, 'capacity_portion', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (4, 'things', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (5, 'capacity', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (5, 'capacity_portion', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (5, 'things', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (8, 'capacity', 10);
INSERT INTO domain_resources (service_id, name, quota) VALUES (8, 'capacity_portion', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (8, 'things', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (9, 'capacity', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (9, 'capacity_portion', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (9, 'things', 0);

INSERT INTO domain_services (id, domain_id, type) VALUES (1, 1, 'centralized');
INSERT INTO domain_services (id, domain_id, type) VALUES (2, 1, 'shared');
INSERT INTO domain_services (id, domain_id, type) VALUES (4, 2, 'centralized');
INSERT INTO domain_services (id, domain_id, type) VALUES (5, 2, 'shared');
INSERT INTO domain_services (id, domain_id, type) VALUES (8, 1, 'unshared');
INSERT INTO domain_services (id, domain_id, type) VALUES (9, 2, 'unshared');

INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');
INSERT INTO domains (id, name, uuid) VALUES (2, 'france', 'uuid-for-france');

INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state, serialized_metrics, checked_at, scrape_error_message, rates_checked_at, rates_scrape_error_message) VALUES (1, 1, 'centralized', NULL, FALSE, 0, NULL, FALSE, 0, '', '', NULL, '', NULL, '');
INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state, serialized_metrics, checked_at, scrape_error_message, rates_checked_at, rates_scrape_error_message) VALUES (11, 1, 'shared', NULL, FALSE, 0, NULL, FALSE, 0, '', '', NULL, '', NULL, '');
INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state, serialized_metrics, checked_at, scrape_error_message, rates_checked_at, rates_scrape_error_message) VALUES (12, 2, 'shared', NULL, FALSE, 0, NULL, FALSE, 0, '', '', NULL, '', NULL, '');
INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state, serialized_metrics, checked_at, scrape_error_message, rates_checked_at, rates_scrape_error_message) VALUES (13, 3, 'shared', NULL, FALSE, 0, NULL, FALSE, 0, '', '', NULL, '', NULL, '');
INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state, serialized_metrics, checked_at, scrape_error_message, rates_checked_at, rates_scrape_error_message) VALUES (3, 1, 'unshared', NULL, FALSE, 0, NULL, FALSE, 0, '', '', NULL, '', NULL, '');
INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state, serialized_metrics, checked_at, scrape_error_message, rates_checked_at, rates_scrape_error_message) VALUES (4, 2, 'centralized', NULL, FALSE, 0, NULL, FALSE, 0, '', '', NULL, '', NULL, '');
INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state, serialized_metrics, checked_at, scrape_error_message, rates_checked_at, rates_scrape_error_message) VALUES (6, 2, 'unshared', NULL, FALSE, 0, NULL, FALSE, 0, '', '', NULL, '', NULL, '');
INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state, serialized_metrics, checked_at, scrape_error_message, rates_checked_at, rates_scrape_error_message) VALUES (7, 3, 'centralized', NULL, FALSE, 0, NULL, FALSE, 0, '', '', NULL, '', NULL, '');
INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state, serialized_metrics, checked_at, scrape_error_message, rates_checked_at, rates_scrape_error_message) VALUES (9, 3, 'unshared', NULL, FALSE, 0, NULL, FALSE, 0, '', '', NULL, '', NULL, '');

INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany', FALSE);
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin', FALSE);
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (3, 2, 'paris', 'uuid-for-paris', 'uuid-for-france', FALSE);
