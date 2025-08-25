INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (1, 1, 'any', 0, 'shared/capacity/any');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (10, 3, 'az-two', 0, 'unshared/capacity/az-two');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (11, 3, 'total', 0, 'unshared/capacity/total');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (12, 3, 'unknown', 0, 'unshared/capacity/unknown');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (13, 4, 'any', 0, 'unshared/things/any');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (14, 4, 'total', 0, 'unshared/things/total');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (2, 1, 'az-one', 0, 'shared/capacity/az-one');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (3, 1, 'az-two', 0, 'shared/capacity/az-two');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (4, 1, 'total', 0, 'shared/capacity/total');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (5, 1, 'unknown', 0, 'shared/capacity/unknown');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (6, 2, 'any', 0, 'shared/things/any');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (7, 2, 'total', 0, 'shared/things/total');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (8, 3, 'any', 0, 'unshared/capacity/any');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (9, 3, 'az-one', 0, 'unshared/capacity/az-one');

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

INSERT INTO resources (id, service_id, name, liquid_version, unit, topology, has_capacity, needs_resource_demand, has_quota, path) VALUES (1, 1, 'capacity', 1, 'B', 'az-aware', TRUE, TRUE, TRUE, 'shared/capacity');
INSERT INTO resources (id, service_id, name, liquid_version, topology, has_quota, path) VALUES (2, 1, 'things', 1, 'flat', TRUE, 'shared/things');
INSERT INTO resources (id, service_id, name, liquid_version, unit, topology, has_capacity, needs_resource_demand, has_quota, path) VALUES (3, 2, 'capacity', 1, 'B', 'az-aware', TRUE, TRUE, TRUE, 'unshared/capacity');
INSERT INTO resources (id, service_id, name, liquid_version, topology, has_quota, path) VALUES (4, 2, 'things', 1, 'flat', TRUE, 'unshared/things');

INSERT INTO services (id, type, next_scrape_at, liquid_version) VALUES (1, 'shared', 0, 1);
INSERT INTO services (id, type, next_scrape_at, liquid_version) VALUES (2, 'unshared', 0, 1);
