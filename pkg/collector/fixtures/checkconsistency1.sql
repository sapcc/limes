INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (1, 'west', 'unshared', 0);
INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (2, 'shared', 'whatever', 0);
INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (3, 'west', 'shared', 0);

INSERT INTO domains (id, cluster_id, name, uuid) VALUES (1, 'west', 'germany', 'uuid-for-germany');
INSERT INTO domains (id, cluster_id, name, uuid) VALUES (2, 'west', 'france', 'uuid-for-france');

INSERT INTO domain_services (id, domain_id, type) VALUES (2, 1, 'shared');
INSERT INTO domain_services (id, domain_id, type) VALUES (4, 2, 'shared');
INSERT INTO domain_services (id, domain_id, type) VALUES (5, 1, 'whatever');

INSERT INTO domain_resources (service_id, name, quota) VALUES (2, 'capacity', 200);

INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany', FALSE);
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin', FALSE);
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (3, 2, 'paris', 'uuid-for-paris', 'uuid-for-france', FALSE);

INSERT INTO project_services (id, project_id, type, scraped_at, stale) VALUES (1, 1, 'unshared', NULL, FALSE);
INSERT INTO project_services (id, project_id, type, scraped_at, stale) VALUES (3, 2, 'unshared', NULL, FALSE);
INSERT INTO project_services (id, project_id, type, scraped_at, stale) VALUES (5, 3, 'unshared', NULL, FALSE);
INSERT INTO project_services (id, project_id, type, scraped_at, stale) VALUES (6, 1, 'whatever', NULL, FALSE);

INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (1, 'capacity', 20, 0, 0, '', 0, NULL);
