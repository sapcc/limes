INSERT INTO domains (id, cluster_id, name, uuid) VALUES (1, 'west', 'germany', 'uuid-for-germany');

INSERT INTO domain_services (id, domain_id, type) VALUES (1, 1, 'unittest');

INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany');

INSERT INTO project_services (id, project_id, type, scraped_at, stale) VALUES (1, 1, 'unittest', 1, FALSE);

INSERT INTO project_resources (service_id, name, quota, usage, backend_quota) VALUES (1, 'capacity', 0, 0, 100);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota) VALUES (1, 'things', 0, 23, 42);
