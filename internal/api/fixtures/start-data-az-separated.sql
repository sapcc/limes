CREATE OR REPLACE FUNCTION unix(i integer) RETURNS timestamp AS $$ SELECT TO_TIMESTAMP(i) AT TIME ZONE 'Etc/UTC' $$ LANGUAGE SQL;

INSERT INTO cluster_services (id, type, next_scrape_at, liquid_version) VALUES (1, 'shared', UNIX(1000), 1);

INSERT INTO cluster_resources (id, service_id, name, liquid_version, unit, topology, has_quota) VALUES (5, 1, 'capacity_az_separated', 1, 'B', 'az-separated', TRUE);

INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');

INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany');

INSERT INTO project_services (id, project_id, type, scraped_at, checked_at) VALUES (1, 1, 'shared',   UNIX(22), UNIX(22));

INSERT INTO project_resources (id, service_id, name, quota, backend_quota) VALUES (1,  1, 'capacity_az_separated', NULL, NULL);

-- AZ separated resource does not include any az.
INSERT INTO project_az_resources (id, resource_id, az, backend_quota, quota, usage, physical_usage, subresources) VALUES (1,  1,  'az-one', 5, 5,    1, NULL, '');
INSERT INTO project_az_resources (id, resource_id, az, backend_quota, quota, usage, physical_usage, subresources) VALUES (2,  1,  'az-two', 5, 5,    1, NULL, '');
