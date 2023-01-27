INSERT INTO domain_resources (service_id, name, quota) VALUES (1, 'capacity', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (1, 'capacity_portion', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (1, 'things', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (2, 'capacity', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (2, 'capacity_portion', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (2, 'things', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (3, 'capacity', 20);
INSERT INTO domain_resources (service_id, name, quota) VALUES (3, 'capacity_portion', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (3, 'things', 10);
INSERT INTO domain_resources (service_id, name, quota) VALUES (4, 'capacity', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (4, 'capacity_portion', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (4, 'things', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (5, 'capacity', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (5, 'capacity_portion', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (5, 'things', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (6, 'capacity', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (6, 'capacity_portion', 0);
INSERT INTO domain_resources (service_id, name, quota) VALUES (6, 'things', 0);

INSERT INTO domain_services (id, domain_id, type) VALUES (1, 1, 'centralized');
INSERT INTO domain_services (id, domain_id, type) VALUES (2, 1, 'shared');
INSERT INTO domain_services (id, domain_id, type) VALUES (3, 1, 'unshared');
INSERT INTO domain_services (id, domain_id, type) VALUES (4, 2, 'centralized');
INSERT INTO domain_services (id, domain_id, type) VALUES (5, 2, 'shared');
INSERT INTO domain_services (id, domain_id, type) VALUES (6, 2, 'unshared');

INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');
INSERT INTO domains (id, name, uuid) VALUES (2, 'france', 'uuid-for-france');

INSERT INTO project_services (id, project_id, type) VALUES (1, 1, 'centralized');
INSERT INTO project_services (id, project_id, type) VALUES (2, 1, 'shared');
INSERT INTO project_services (id, project_id, type) VALUES (3, 1, 'unshared');
INSERT INTO project_services (id, project_id, type) VALUES (4, 2, 'centralized');
INSERT INTO project_services (id, project_id, type) VALUES (5, 2, 'shared');
INSERT INTO project_services (id, project_id, type) VALUES (6, 2, 'unshared');
INSERT INTO project_services (id, project_id, type) VALUES (7, 3, 'centralized');
INSERT INTO project_services (id, project_id, type) VALUES (8, 3, 'shared');
INSERT INTO project_services (id, project_id, type) VALUES (9, 3, 'unshared');

INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany', FALSE);
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin', FALSE);
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (3, 2, 'paris', 'uuid-for-paris', 'uuid-for-france', FALSE);