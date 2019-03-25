INSERT INTO domains (id, cluster_id, name, uuid) VALUES (1, 'west', 'germany', 'uuid-for-germany');

INSERT INTO domain_services (id, domain_id, type) VALUES (1, 1, 'autoapprovaltest');

INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany', FALSE);

INSERT INTO project_services (id, project_id, type, scraped_at, stale) VALUES (1, 1, 'autoapprovaltest', 3, FALSE);

INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (1, 'approve', 10, 0, 20, '', 10, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (1, 'noapprove', 0, 0, 30, '', 0, NULL);
