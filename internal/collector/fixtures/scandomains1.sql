INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, path) VALUES (1, 1, 'any', 0, 'shared/capacity/any');
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, path) VALUES (10, 4, 'any', 0, 'unshared/things/any');
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, path) VALUES (2, 1, 'az-one', 0, 'shared/capacity/az-one');
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, path) VALUES (3, 1, 'az-two', 0, 'shared/capacity/az-two');
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, path) VALUES (4, 1, 'unknown', 0, 'shared/capacity/unknown');
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, path) VALUES (5, 2, 'any', 0, 'shared/things/any');
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, path) VALUES (6, 3, 'any', 0, 'unshared/capacity/any');
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, path) VALUES (7, 3, 'az-one', 0, 'unshared/capacity/az-one');
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, path) VALUES (8, 3, 'az-two', 0, 'unshared/capacity/az-two');
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, path) VALUES (9, 3, 'unknown', 0, 'unshared/capacity/unknown');

INSERT INTO cluster_resources (id, service_id, name, liquid_version, unit, topology, has_capacity, needs_resource_demand, has_quota, path) VALUES (1, 1, 'capacity', 1, 'B', 'az-aware', TRUE, TRUE, TRUE, 'shared/capacity');
INSERT INTO cluster_resources (id, service_id, name, liquid_version, topology, has_quota, path) VALUES (2, 1, 'things', 1, 'flat', TRUE, 'shared/things');
INSERT INTO cluster_resources (id, service_id, name, liquid_version, unit, topology, has_capacity, needs_resource_demand, has_quota, path) VALUES (3, 2, 'capacity', 1, 'B', 'az-aware', TRUE, TRUE, TRUE, 'unshared/capacity');
INSERT INTO cluster_resources (id, service_id, name, liquid_version, topology, has_quota, path) VALUES (4, 2, 'things', 1, 'flat', TRUE, 'unshared/things');

INSERT INTO cluster_services (id, type, next_scrape_at, liquid_version) VALUES (1, 'shared', 0, 1);
INSERT INTO cluster_services (id, type, next_scrape_at, liquid_version) VALUES (2, 'unshared', 0, 1);

INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');
INSERT INTO domains (id, name, uuid) VALUES (2, 'france', 'uuid-for-france');

INSERT INTO project_services (id, project_id, service_id, stale, next_scrape_at) VALUES (1, 1, 1, TRUE, 0);
INSERT INTO project_services (id, project_id, service_id, stale, next_scrape_at) VALUES (2, 1, 2, TRUE, 0);
INSERT INTO project_services (id, project_id, service_id, stale, next_scrape_at) VALUES (3, 2, 1, TRUE, 0);
INSERT INTO project_services (id, project_id, service_id, stale, next_scrape_at) VALUES (4, 2, 2, TRUE, 0);
INSERT INTO project_services (id, project_id, service_id, stale, next_scrape_at) VALUES (5, 3, 1, TRUE, 0);
INSERT INTO project_services (id, project_id, service_id, stale, next_scrape_at) VALUES (6, 3, 2, TRUE, 0);

INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (3, 2, 'paris', 'uuid-for-paris', 'uuid-for-france');
