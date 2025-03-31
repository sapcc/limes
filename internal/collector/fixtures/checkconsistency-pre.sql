INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');
INSERT INTO domains (id, name, uuid) VALUES (2, 'france', 'uuid-for-france');

INSERT INTO project_services (id, project_id, type, stale, rates_stale, next_scrape_at, rates_next_scrape_at) VALUES (1, 1, 'shared', TRUE, TRUE, 0, 0);
INSERT INTO project_services (id, project_id, type, stale, rates_stale, next_scrape_at, rates_next_scrape_at) VALUES (2, 1, 'unshared', TRUE, TRUE, 0, 0);
INSERT INTO project_services (id, project_id, type, stale, rates_stale, next_scrape_at, rates_next_scrape_at) VALUES (3, 2, 'shared', TRUE, TRUE, 0, 0);
INSERT INTO project_services (id, project_id, type, stale, rates_stale, next_scrape_at, rates_next_scrape_at) VALUES (4, 2, 'unshared', TRUE, TRUE, 0, 0);
INSERT INTO project_services (id, project_id, type, stale, rates_stale, next_scrape_at, rates_next_scrape_at) VALUES (5, 3, 'shared', TRUE, TRUE, 0, 0);
INSERT INTO project_services (id, project_id, type, stale, rates_stale, next_scrape_at, rates_next_scrape_at) VALUES (6, 3, 'unshared', TRUE, TRUE, 0, 0);

INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (3, 2, 'paris', 'uuid-for-paris', 'uuid-for-france');
