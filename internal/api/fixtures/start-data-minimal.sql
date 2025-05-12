CREATE OR REPLACE FUNCTION unix(i integer) RETURNS timestamp AS $$ SELECT TO_TIMESTAMP(i) AT TIME ZONE 'Etc/UTC' $$ LANGUAGE SQL;

-- two services, one shared, one unshared
INSERT INTO cluster_services (id, type, liquid_version) VALUES (1, 'unshared', 1);
INSERT INTO cluster_services (id, type, liquid_version) VALUES (2, 'shared', 1);

-- two domains
INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');
INSERT INTO domains (id, name, uuid) VALUES (2, 'france',  'uuid-for-france');
