INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (1, 'west', 'unshared', 0);
INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (2, 'shared', 'shared', 0);

INSERT INTO domains (id, cluster_id, name, uuid) VALUES (1, 'west', 'germany', 'uuid-for-germany');
INSERT INTO domains (id, cluster_id, name, uuid) VALUES (2, 'west', 'france', 'uuid-for-france');

INSERT INTO domain_services (id, domain_id, type) VALUES (1, 1, 'unshared');
INSERT INTO domain_services (id, domain_id, type) VALUES (2, 1, 'shared');
INSERT INTO domain_services (id, domain_id, type) VALUES (3, 2, 'unshared');
INSERT INTO domain_services (id, domain_id, type) VALUES (4, 2, 'shared');

INSERT INTO projects (id, domain_id, name, uuid) VALUES (1, 1, 'berlin', 'uuid-for-berlin');
INSERT INTO projects (id, domain_id, name, uuid) VALUES (2, 1, 'dresden', 'uuid-for-dresden');
INSERT INTO projects (id, domain_id, name, uuid) VALUES (3, 2, 'paris', 'uuid-for-paris');

INSERT INTO project_services (id, project_id, type, scraped_at, stale) VALUES (1, 1, 'unshared', NULL, FALSE);
INSERT INTO project_services (id, project_id, type, scraped_at, stale) VALUES (2, 1, 'shared', NULL, FALSE);
INSERT INTO project_services (id, project_id, type, scraped_at, stale) VALUES (3, 2, 'unshared', NULL, FALSE);
INSERT INTO project_services (id, project_id, type, scraped_at, stale) VALUES (4, 2, 'shared', NULL, FALSE);
INSERT INTO project_services (id, project_id, type, scraped_at, stale) VALUES (5, 3, 'unshared', NULL, FALSE);
INSERT INTO project_services (id, project_id, type, scraped_at, stale) VALUES (6, 3, 'shared', NULL, FALSE);
