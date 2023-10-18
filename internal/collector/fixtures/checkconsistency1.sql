INSERT INTO cluster_services (id, type) VALUES (2, 'unshared');
INSERT INTO cluster_services (id, type) VALUES (3, 'whatever');

INSERT INTO domain_resources (id, service_id, name, quota) VALUES (1, 1, 'capacity', 200);
INSERT INTO domain_resources (id, service_id, name, quota) VALUES (2, 1, 'capacity_portion', 0);
INSERT INTO domain_resources (id, service_id, name, quota) VALUES (3, 1, 'things', 0);
INSERT INTO domain_resources (id, service_id, name, quota) VALUES (7, 3, 'capacity', 0);
INSERT INTO domain_resources (id, service_id, name, quota) VALUES (8, 3, 'capacity_portion', 0);
INSERT INTO domain_resources (id, service_id, name, quota) VALUES (9, 3, 'things', 0);

INSERT INTO domain_services (id, domain_id, type) VALUES (1, 1, 'shared');
INSERT INTO domain_services (id, domain_id, type) VALUES (3, 2, 'shared');
INSERT INTO domain_services (id, domain_id, type) VALUES (5, 1, 'whatever');

INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');
INSERT INTO domains (id, name, uuid) VALUES (2, 'france', 'uuid-for-france');

INSERT INTO project_services (id, project_id, type, next_scrape_at, rates_next_scrape_at) VALUES (2, 1, 'unshared', 0, 0);
INSERT INTO project_services (id, project_id, type, next_scrape_at, rates_next_scrape_at) VALUES (4, 2, 'unshared', 0, 0);
INSERT INTO project_services (id, project_id, type, next_scrape_at, rates_next_scrape_at) VALUES (6, 3, 'unshared', 0, 0);
INSERT INTO project_services (id, project_id, type, next_scrape_at, rates_next_scrape_at) VALUES (7, 1, 'whatever', 0, 0);

INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany', FALSE);
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin', FALSE);
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (3, 2, 'paris', 'uuid-for-paris', 'uuid-for-france', FALSE);
