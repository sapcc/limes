INSERT INTO domain_resources (id, service_id, name, quota) VALUES (1, 1, 'capacity', 0);
INSERT INTO domain_resources (id, service_id, name, quota) VALUES (2, 1, 'capacity_portion', 0);
INSERT INTO domain_resources (id, service_id, name, quota) VALUES (3, 1, 'things', 0);

INSERT INTO domain_services (id, domain_id, type) VALUES (1, 1, 'unittest');

INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');

INSERT INTO project_services (id, project_id, type, next_scrape_at, rates_next_scrape_at) VALUES (1, 1, 'unittest', 0, 0);
INSERT INTO project_services (id, project_id, type, next_scrape_at, rates_next_scrape_at) VALUES (2, 2, 'unittest', 0, 0);

INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany', FALSE);
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin');
