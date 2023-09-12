CREATE OR REPLACE FUNCTION unix(i integer) RETURNS timestamp AS $$ SELECT TO_TIMESTAMP(i) AT TIME ZONE 'Etc/UTC' $$ LANGUAGE SQL;

-- two services, one shared, one unshared
INSERT INTO cluster_services (id, type) VALUES (1, 'unshared');
INSERT INTO cluster_services (id, type) VALUES (2, 'shared');

-- two domains
INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');
INSERT INTO domains (id, name, uuid) VALUES (2, 'france',  'uuid-for-france');

-- domain_services is fully populated (as ensured by the collector's consistency check)
INSERT INTO domain_services (id, domain_id, type) VALUES (1, 1, 'unshared');
INSERT INTO domain_services (id, domain_id, type) VALUES (2, 1, 'shared');
INSERT INTO domain_services (id, domain_id, type) VALUES (3, 2, 'unshared');
INSERT INTO domain_services (id, domain_id, type) VALUES (4, 2, 'shared');
