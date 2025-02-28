CREATE OR REPLACE FUNCTION unix(i integer) RETURNS timestamp AS $$ SELECT TO_TIMESTAMP(i) AT TIME ZONE 'Etc/UTC' $$ LANGUAGE SQL;

INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');
INSERT INTO projects(id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'waldorf', 'uuid-for-waldorf', 'uuid-for-germany');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (2, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-waldorf');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (3, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (4, 1, 'frankfurt', 'uuid-for-frankfurt', 'uuid-for-dresden');
INSERT INTO project_mail_notifications (id, project_id, subject, body, next_submission_at) VALUES (1, 1, 'dummy', 'dummy', UNIX(0));
INSERT INTO project_mail_notifications (id, project_id, subject, body, next_submission_at) VALUES (2, 2, 'dummy', 'dummy', UNIX(86400));
INSERT INTO project_mail_notifications (id, project_id, subject, body, next_submission_at) VALUES (3, 3, 'dummy', 'dummy', UNIX(172800));
INSERT INTO project_mail_notifications (id, project_id, subject, body, next_submission_at) VALUES (4, 4, 'dummy', 'dummy', UNIX(259200));
