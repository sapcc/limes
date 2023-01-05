INSERT INTO domain_resources (service_id, name, quota) VALUES (1, 'capacity', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (1, 'capacity_portion', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (1, 'things', 0);

INSERT INTO domain_services (id, domain_id, type) VALUES (1, 1, 'centralized');

INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');

INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state, serialized_metrics, checked_at, scrape_error_message, rates_checked_at, rates_scrape_error_message) VALUES (1, 1, 'centralized', NULL, FALSE, 0, NULL, FALSE, 0, '', '', NULL, '', NULL, '');

INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany', FALSE);
