INSERT INTO domains (id, cluster_id, name, uuid) VALUES (1, 'cluster-id-test', 'Default', '2131d24fee484da9be8671aa276360e0');

INSERT INTO domain_services (id, domain_id, name) VALUES (1, 1, 'foo');
INSERT INTO domain_services (id, domain_id, name) VALUES (2, 1, 'bar');

INSERT INTO projects (id, domain_id, name, uuid) VALUES (1, 1, 'foo', 'dd53fc9c38d740c6b7889424e740e194');
INSERT INTO projects (id, domain_id, name, uuid) VALUES (2, 1, 'bar', '003645ff7b534b8ab612885ff7653526');

INSERT INTO project_services (id, project_id, name, scraped_at, stale) VALUES (1, 1, 'foo', NULL, FALSE);
INSERT INTO project_services (id, project_id, name, scraped_at, stale) VALUES (2, 1, 'bar', NULL, FALSE);
INSERT INTO project_services (id, project_id, name, scraped_at, stale) VALUES (3, 2, 'foo', NULL, FALSE);
INSERT INTO project_services (id, project_id, name, scraped_at, stale) VALUES (4, 2, 'bar', NULL, FALSE);
