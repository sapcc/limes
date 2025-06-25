INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (10, 4, 'any', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (6, 3, 'any', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (7, 3, 'az-one', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (8, 3, 'az-two', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (9, 3, 'unknown', 0);

INSERT INTO cluster_resources (id, service_id, name, liquid_version, unit, topology, has_capacity, needs_resource_demand, has_quota) VALUES (3, 2, 'capacity', 1, 'B', 'az-aware', TRUE, TRUE, TRUE);
INSERT INTO cluster_resources (id, service_id, name, liquid_version, topology, has_quota) VALUES (4, 2, 'things', 1, 'flat', TRUE);

INSERT INTO cluster_services (id, type, next_scrape_at, liquid_version) VALUES (2, 'unshared', 0, 1);
INSERT INTO cluster_services (id, type, next_scrape_at) VALUES (3, 'whatever', 3600);

INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');
INSERT INTO domains (id, name, uuid) VALUES (2, 'france', 'uuid-for-france');

INSERT INTO project_services (id, project_id, type, stale, next_scrape_at) VALUES (10, 3, 'shared', TRUE, 7200);
INSERT INTO project_services (id, project_id, type, stale, next_scrape_at) VALUES (11, 1, 'whatever', TRUE, 10800);
INSERT INTO project_services (id, project_id, type, stale, next_scrape_at) VALUES (12, 2, 'whatever', TRUE, 10800);
INSERT INTO project_services (id, project_id, type, stale, next_scrape_at) VALUES (13, 3, 'whatever', TRUE, 10800);
INSERT INTO project_services (id, project_id, type, stale, next_scrape_at) VALUES (2, 1, 'unshared', TRUE, 0);
INSERT INTO project_services (id, project_id, type, stale, next_scrape_at) VALUES (4, 2, 'unshared', TRUE, 0);
INSERT INTO project_services (id, project_id, type, stale, next_scrape_at) VALUES (6, 3, 'unshared', TRUE, 0);
INSERT INTO project_services (id, project_id, type, stale, next_scrape_at) VALUES (8, 1, 'shared', TRUE, 7200);
INSERT INTO project_services (id, project_id, type, stale, next_scrape_at) VALUES (9, 2, 'shared', TRUE, 7200);

INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (3, 2, 'paris', 'uuid-for-paris', 'uuid-for-france');
