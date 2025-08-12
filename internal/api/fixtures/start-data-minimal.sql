CREATE OR REPLACE FUNCTION unix(i integer) RETURNS timestamp AS $$ SELECT TO_TIMESTAMP(i) AT LOCAL $$ LANGUAGE SQL;

-- two services, one shared, one unshared
INSERT INTO services (id, type, liquid_version) VALUES (1, 'unshared', 1);
INSERT INTO services (id, type, liquid_version) VALUES (2, 'shared', 1);

-- all services have the resources "things" and "capacity"
INSERT INTO resources (id, service_id, name, liquid_version, topology, has_quota, path) VALUES (1, 1, 'things', 1, 'flat', TRUE, 'unshared/things');
INSERT INTO resources (id, service_id, name, liquid_version, topology, has_quota, path) VALUES (2, 2, 'things', 1, 'flat', TRUE, 'shared/things');
INSERT INTO resources (id, service_id, name, liquid_version, unit, topology, has_capacity, needs_resource_demand, has_quota, path) VALUES (3, 2, 'capacity', 1, 'B', 'az-aware', TRUE, TRUE, TRUE, 'shared/capacity');
INSERT INTO resources (id, service_id, name, liquid_version, unit, topology, has_capacity, needs_resource_demand, has_quota, path) VALUES (4, 1, 'capacity', 1, 'B', 'az-aware', TRUE, TRUE, TRUE, 'unshared/capacity');

-- all resources have the az=any
INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (1, 1, 'any', 0, 'unshared/things/any');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (2, 2, 'any', 0, 'shared/things/any');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (3, 3, 'any', 0, 'shared/capacity/any');
INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (4, 4, 'any', 0, 'unshared/capacity/any');

-- two domains
INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');
INSERT INTO domains (id, name, uuid) VALUES (2, 'france',  'uuid-for-france');
