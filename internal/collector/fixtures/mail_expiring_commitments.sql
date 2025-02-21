CREATE OR REPLACE FUNCTION unix(i integer) RETURNS timestamp AS $$ SELECT TO_TIMESTAMP(i) AT TIME ZONE 'Etc/UTC' $$ LANGUAGE SQL;

INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');

INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin');

INSERT INTO project_services (id, project_id, type) VALUES (1, 1, 'first');
INSERT INTO project_services (id, project_id, type) VALUES (2, 2, 'first');

INSERT INTO project_resources (id, service_id, name) VALUES (1,  1, 'things');
INSERT INTO project_resources (id, service_id, name) VALUES (2,  2, 'things');

INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (1, 1,  'az-one', 0);
INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (2, 1,  'az-two', 0);
INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (3, 2,  'az-one', 0);
INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (4, 2,  'az-two', 0);

-- active/planned commitments should be ignored
INSERT INTO project_commitments (id, az_resource_id, amount, created_at, creator_uuid, creator_name, confirm_by, duration, expires_at, state) VALUES (1, 1, 10, UNIX(0), 'dummy', 'dummy', UNIX(86400), '1 year', UNIX(31536000), 'planned');
INSERT INTO project_commitments (id, az_resource_id, amount, created_at, creator_uuid, creator_name, duration, expires_at, state) VALUES (2, 1, 10, UNIX(0), 'dummy', 'dummy', '1 year', UNIX(31536000), 'active');
INSERT INTO project_commitments (id, az_resource_id, amount, created_at, creator_uuid, creator_name, confirm_by, duration, expires_at, state) VALUES (3, 1, 10, UNIX(0), 'dummy', 'dummy', UNIX(5097600), '10 days', UNIX(5875200), 'planned');

-- expiring commitments for each project
INSERT INTO project_commitments (id, az_resource_id, amount, created_at, creator_uuid, creator_name, duration, expires_at, state) VALUES (4, 1, 5, UNIX(0), 'dummy', 'dummy', '1 year', UNIX(0), 'expired');
INSERT INTO project_commitments (id, az_resource_id, amount, created_at, creator_uuid, creator_name, duration, expires_at, state) VALUES (5, 2, 10, UNIX(0), 'dummy', 'dummy', '1 year', UNIX(0), 'expired');
INSERT INTO project_commitments (id, az_resource_id, amount, created_at, creator_uuid, creator_name, duration, expires_at, state) VALUES (6, 3, 5, UNIX(0), 'dummy', 'dummy', '1 year', UNIX(2678400), 'active');
INSERT INTO project_commitments (id, az_resource_id, amount, created_at, creator_uuid, creator_name, duration, expires_at, state) VALUES (7, 4, 10, UNIX(0), 'dummy', 'dummy', '1 year', UNIX(2678400), 'active');

-- expiring short-term commitments should not be queued and be marked as notified
INSERT INTO project_commitments (id, az_resource_id, amount, created_at, creator_uuid, creator_name, confirm_by, duration, expires_at, state) VALUES (8, 1, 10, UNIX(0), 'dummy', 'dummy', UNIX(86400), '10 days', UNIX(950400), 'planned');
INSERT INTO project_commitments (id, az_resource_id, amount, created_at, creator_uuid, creator_name, confirmed_at, duration, expires_at, state) VALUES (9, 1, 10, UNIX(0), 'dummy', 'dummy', UNIX(0), '10 days', UNIX(777600), 'active');
