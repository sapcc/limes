INSERT INTO domains (id, cluster_id, name, uuid) VALUES (1, 'west', 'germany-changed', 'uuid-for-germany');

INSERT INTO domain_services (id, domain_id, type) VALUES (1, 1, 'unshared');
INSERT INTO domain_services (id, domain_id, type) VALUES (2, 1, 'shared');

INSERT INTO projects (id, domain_id, name, uuid) VALUES (1, 1, 'berlin-changed', 'uuid-for-berlin');
INSERT INTO projects (id, domain_id, name, uuid) VALUES (2, 1, 'dresden', 'uuid-for-dresden');

INSERT INTO project_services (id, project_id, type, scraped_at, stale) VALUES (1, 1, 'unshared', NULL, FALSE);
INSERT INTO project_services (id, project_id, type, scraped_at, stale) VALUES (2, 1, 'shared', NULL, FALSE);
INSERT INTO project_services (id, project_id, type, scraped_at, stale) VALUES (3, 2, 'unshared', NULL, FALSE);
INSERT INTO project_services (id, project_id, type, scraped_at, stale) VALUES (4, 2, 'shared', NULL, FALSE);
