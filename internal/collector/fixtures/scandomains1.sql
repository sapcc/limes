INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (1, 1, 'any', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (10, 4, 'any', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (2, 1, 'az-one', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (3, 1, 'az-two', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (4, 1, 'unknown', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (5, 2, 'any', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (6, 3, 'any', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (7, 3, 'az-one', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (8, 3, 'az-two', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (9, 3, 'unknown', 0);

INSERT INTO cluster_resources (id, service_id, name, liquid_version, unit, topology, has_capacity, needs_resource_demand, has_quota) VALUES (1, 1, 'capacity', 1, 'B', 'az-aware', TRUE, TRUE, TRUE);
INSERT INTO cluster_resources (id, service_id, name, liquid_version, topology, has_quota) VALUES (2, 1, 'things', 1, 'flat', TRUE);
INSERT INTO cluster_resources (id, service_id, name, liquid_version, unit, topology, has_capacity, needs_resource_demand, has_quota) VALUES (3, 2, 'capacity', 1, 'B', 'az-aware', TRUE, TRUE, TRUE);
INSERT INTO cluster_resources (id, service_id, name, liquid_version, topology, has_quota) VALUES (4, 2, 'things', 1, 'flat', TRUE);

INSERT INTO cluster_services (id, type, next_scrape_at, liquid_version) VALUES (1, 'shared', 0, 1);
INSERT INTO cluster_services (id, type, next_scrape_at, liquid_version) VALUES (2, 'unshared', 0, 1);

INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');
INSERT INTO domains (id, name, uuid) VALUES (2, 'france', 'uuid-for-france');

INSERT INTO project_services_v2 (id, project_id, service_id, stale, next_scrape_at) VALUES (1, 1, 1, TRUE, 0);
INSERT INTO project_services_v2 (id, project_id, service_id, stale, next_scrape_at) VALUES (2, 1, 2, TRUE, 0);
INSERT INTO project_services_v2 (id, project_id, service_id, stale, next_scrape_at) VALUES (3, 2, 1, TRUE, 0);
INSERT INTO project_services_v2 (id, project_id, service_id, stale, next_scrape_at) VALUES (4, 2, 2, TRUE, 0);
INSERT INTO project_services_v2 (id, project_id, service_id, stale, next_scrape_at) VALUES (5, 3, 1, TRUE, 0);
INSERT INTO project_services_v2 (id, project_id, service_id, stale, next_scrape_at) VALUES (6, 3, 2, TRUE, 0);

INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (3, 2, 'paris', 'uuid-for-paris', 'uuid-for-france');
