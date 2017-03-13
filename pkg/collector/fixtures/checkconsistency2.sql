INSERT INTO domains (id, cluster_id, name, uuid) VALUES (1, 'cluster-id-test', 'Default', '2131d24fee484da9be8671aa276360e0');
INSERT INTO domains (id, cluster_id, name, uuid) VALUES (2, 'cluster-id-test', 'Example', 'a2f0d9a6a8a0410f9881335f1fe0b538');

INSERT INTO domain_services (id, domain_id, name) VALUES (2, 1, 'bar');
INSERT INTO domain_services (id, domain_id, name) VALUES (4, 2, 'bar');
INSERT INTO domain_services (id, domain_id, name) VALUES (5, 1, 'foo');
INSERT INTO domain_services (id, domain_id, name) VALUES (6, 2, 'foo');

INSERT INTO projects (id, domain_id, name, uuid) VALUES (1, 1, 'foo', 'dd53fc9c38d740c6b7889424e740e194');
INSERT INTO projects (id, domain_id, name, uuid) VALUES (2, 1, 'bar', '003645ff7b534b8ab612885ff7653526');
INSERT INTO projects (id, domain_id, name, uuid) VALUES (3, 2, 'qux', 'ed5867497beb40c69f829837639d873d');

INSERT INTO project_services (id, project_id, name, scraped_at, stale) VALUES (1, 1, 'foo', NULL, FALSE);
INSERT INTO project_services (id, project_id, name, scraped_at, stale) VALUES (3, 2, 'foo', NULL, FALSE);
INSERT INTO project_services (id, project_id, name, scraped_at, stale) VALUES (5, 3, 'foo', NULL, FALSE);
INSERT INTO project_services (id, project_id, name, scraped_at, stale) VALUES (6, 1, 'bar', NULL, FALSE);
INSERT INTO project_services (id, project_id, name, scraped_at, stale) VALUES (7, 2, 'bar', NULL, FALSE);
INSERT INTO project_services (id, project_id, name, scraped_at, stale) VALUES (8, 3, 'bar', NULL, FALSE);
