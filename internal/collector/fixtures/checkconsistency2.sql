INSERT INTO cluster_services (id, type) VALUES (2, 'unshared');
INSERT INTO cluster_services (id, type) VALUES (4, 'shared');

INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');
INSERT INTO domains (id, name, uuid) VALUES (2, 'france', 'uuid-for-france');

INSERT INTO project_services (id, project_id, type, next_scrape_at, rates_next_scrape_at) VALUES (10, 3, 'shared', 7200, 7200);
INSERT INTO project_services (id, project_id, type, next_scrape_at, rates_next_scrape_at) VALUES (2, 1, 'unshared', 0, 0);
INSERT INTO project_services (id, project_id, type, next_scrape_at, rates_next_scrape_at) VALUES (4, 2, 'unshared', 0, 0);
INSERT INTO project_services (id, project_id, type, next_scrape_at, rates_next_scrape_at) VALUES (6, 3, 'unshared', 0, 0);
INSERT INTO project_services (id, project_id, type, next_scrape_at, rates_next_scrape_at) VALUES (8, 1, 'shared', 7200, 7200);
INSERT INTO project_services (id, project_id, type, next_scrape_at, rates_next_scrape_at) VALUES (9, 2, 'shared', 7200, 7200);

INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (3, 2, 'paris', 'uuid-for-paris', 'uuid-for-france');
