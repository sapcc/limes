INSERT INTO domains (id, cluster_id, name, uuid) VALUES (1, 'west', 'germany', 'uuid-for-germany');

INSERT INTO domain_services (id, domain_id, type) VALUES (1, 1, 'unshared');
INSERT INTO domain_services (id, domain_id, type) VALUES (2, 1, 'shared');

INSERT INTO domain_resources (service_id, name, quota) VALUES (1, 'capacity', 20);
INSERT INTO domain_resources (service_id, name, quota) VALUES (1, 'things', 10);

INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany', FALSE);
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin', FALSE);

INSERT INTO project_services (id, project_id, type, scraped_at, stale) VALUES (1, 1, 'unshared', NULL, FALSE);
INSERT INTO project_services (id, project_id, type, scraped_at, stale) VALUES (2, 1, 'shared', NULL, FALSE);
INSERT INTO project_services (id, project_id, type, scraped_at, stale) VALUES (3, 2, 'unshared', NULL, FALSE);
INSERT INTO project_services (id, project_id, type, scraped_at, stale) VALUES (4, 2, 'shared', NULL, FALSE);

INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (1, 'things', 5, 0, 0, '', 5, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (2, 'capacity', 10, 0, 0, '', 10, NULL);
