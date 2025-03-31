INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');

INSERT INTO project_services (id, project_id, type, stale, rates_stale, next_scrape_at, rates_next_scrape_at) VALUES (1, 1, 'unittest', TRUE, TRUE, 0, 0);
INSERT INTO project_services (id, project_id, type, stale, rates_stale, next_scrape_at, rates_next_scrape_at) VALUES (2, 2, 'unittest', TRUE, TRUE, 0, 0);

INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin');
