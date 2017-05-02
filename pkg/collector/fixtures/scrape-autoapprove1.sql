INSERT INTO domains (id, cluster_id, name, uuid) VALUES (1, 'west', 'germany', 'uuid-for-germany');

INSERT INTO domain_services (id, domain_id, type) VALUES (1, 1, 'autoapprovaltest');

INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany');

INSERT INTO project_services (id, project_id, type, scraped_at, stale) VALUES (1, 1, 'autoapprovaltest', 1, FALSE);

INSERT INTO project_resources (service_id, name, quota, usage, backend_quota) VALUES (1, 'approve', 10, 0, 10);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota) VALUES (1, 'noapprove', 0, 0, 20);
