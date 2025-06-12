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
INSERT INTO project_commitments (id, uuid, az_resource_id, amount, created_at, creator_uuid, creator_name, confirm_by, duration, expires_at, state, creation_context_json) VALUES (1, '00000000-0000-0000-0000-000000000001', 1, 10, UNIX(0), 'dummy', 'dummy', UNIX(86400), '1 year', UNIX(31536000), 'planned', '{}'::jsonb);
INSERT INTO project_commitments (id, uuid, az_resource_id, amount, created_at, creator_uuid, creator_name, duration, expires_at, state, creation_context_json) VALUES (2, '00000000-0000-0000-0000-000000000002', 1, 10, UNIX(0), 'dummy', 'dummy', '1 year', UNIX(31536000), 'active', '{}'::jsonb);
INSERT INTO project_commitments (id, uuid, az_resource_id, amount, created_at, creator_uuid, creator_name, confirm_by, duration, expires_at, state, creation_context_json) VALUES (3, '00000000-0000-0000-0000-000000000003', 1, 10, UNIX(0), 'dummy', 'dummy', UNIX(5097600), '10 days', UNIX(5875200), 'planned', '{}'::jsonb);

-- expiring commitments for each project
INSERT INTO project_commitments (id, uuid, az_resource_id, amount, created_at, creator_uuid, creator_name, duration, expires_at, state, creation_context_json) VALUES (4, '00000000-0000-0000-0000-000000000004', 1, 5, UNIX(0), 'dummy', 'dummy', '1 year', UNIX(0), 'active', '{}'::jsonb);
INSERT INTO project_commitments (id, uuid, az_resource_id, amount, created_at, creator_uuid, creator_name, duration, expires_at, state, creation_context_json) VALUES (5, '00000000-0000-0000-0000-000000000005', 2, 10, UNIX(0), 'dummy', 'dummy', '1 year', UNIX(0), 'active', '{}'::jsonb);
-- expiring commitments, marked as one year to make them pass the short-term commitment check, but they will expire within the scrape timeframe.
INSERT INTO project_commitments (id, uuid, az_resource_id, amount, created_at, creator_uuid, creator_name, duration, expires_at, state, creation_context_json) VALUES (6, '00000000-0000-0000-0000-000000000006', 3, 5, UNIX(0), 'dummy', 'dummy', '1 year', UNIX(2246400), 'active', '{}'::jsonb);
INSERT INTO project_commitments (id, uuid, az_resource_id, amount, created_at, creator_uuid, creator_name, duration, expires_at, state, creation_context_json) VALUES (7, '00000000-0000-0000-0000-000000000007', 4, 10, UNIX(0), 'dummy', 'dummy', '1 year', UNIX(2246400), 'active', '{}'::jsonb);

-- expiring short-term commitments should not be queued and be marked as notified
INSERT INTO project_commitments (id, uuid, az_resource_id, amount, created_at, creator_uuid, creator_name, confirm_by, duration, expires_at, state, creation_context_json) VALUES (8, '00000000-0000-0000-0000-000000000008', 1, 10, UNIX(0), 'dummy', 'dummy', UNIX(86400), '10 days', UNIX(950400), 'active', '{}'::jsonb);
INSERT INTO project_commitments (id, uuid, az_resource_id, amount, created_at, creator_uuid, creator_name, confirmed_at, duration, expires_at, state, creation_context_json) VALUES (9, '00000000-0000-0000-0000-000000000009', 1, 10, UNIX(0), 'dummy', 'dummy', UNIX(0), '10 days', UNIX(777600), 'active', '{}'::jsonb);

-- superseded commitments should not be queued for notifications
INSERT INTO project_commitments (id, uuid, az_resource_id, amount, created_at, creator_uuid, creator_name, duration, expires_at, state, creation_context_json) VALUES (10, '00000000-0000-0000-0000-000000000010', 4, 1, UNIX(0), 'dummy', 'dummy', '1 year', UNIX(2246400), 'superseded', '{}'::jsonb);
