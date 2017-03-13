INSERT INTO domains (id, cluster_id, name, uuid) VALUES (1, 'cluster-id-test', 'Default', '2131d24fee484da9be8671aa276360e0');

INSERT INTO domain_services (id, domain_id, name) VALUES (1, 1, 'compute');

INSERT INTO projects (id, domain_id, name, uuid) VALUES (1, 1, 'foo', 'dd53fc9c38d740c6b7889424e740e194');

INSERT INTO project_services (id, project_id, name, scraped_at, stale) VALUES (1, 1, 'compute', 4, FALSE);

INSERT INTO project_resources (service_id, name, quota, usage, backend_quota) VALUES (1, 'cores', 0, 37, 110);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota) VALUES (1, 'instances', 0, 9, 20);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota) VALUES (1, 'ram', 0, 49152, 81920);
