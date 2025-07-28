-- start data for inconsistencies test

-- "cloud" cluster has two domains
INSERT INTO domains (id, name, uuid) VALUES (1, 'germany',  'uuid-for-germany');
INSERT INTO domains (id, name, uuid) VALUES (2, 'pakistan', 'uuid-for-pakistan');

-- "germany" has one project, and "pakistan" has two (lahore is a child project of karachi in order to check
-- correct rendering of the parent_uuid field)
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-germany');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (2, 2, 'karachi', 'uuid-for-karachi', 'uuid-for-pakistan');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (3, 2, 'lahore',  'uuid-for-lahore',  'uuid-for-karachi');

-- all cluster_services for the respective project_services
INSERT INTO cluster_services (id, type, liquid_version) VALUES (1, 'shared', 1);
INSERT INTO cluster_services (id, type, liquid_version) VALUES (2, 'unshared', 1);

-- all cluster_resources for the respective project-resources
INSERT INTO cluster_resources (id, service_id, name, liquid_version, topology, unit) VALUES (1, 1, 'capacity', 1, 'flat', 'B');
INSERT INTO cluster_resources (id, service_id, name, liquid_version, topology) VALUES (2, 1, 'things', 1, 'flat');
INSERT INTO cluster_resources (id, service_id, name, liquid_version, topology) VALUES (3, 2, 'things', 1, 'flat');

-- all cluster_az_resources for the respective project_az_resources
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (1, 1, 'any', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (2, 2, 'any', 0);
INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (3, 3, 'any', 0);

-- project_services is fully populated (as ensured by the collector's consistency check)
INSERT INTO project_services_v2 (id, project_id, service_id, scraped_at, checked_at) VALUES (1, 1, 1, '2018-06-13 15:06:37', '2018-06-13 15:06:37');
INSERT INTO project_services_v2 (id, project_id, service_id, scraped_at, checked_at) VALUES (2, 1, 2, '2018-06-13 15:06:37', '2018-06-13 15:06:37');
INSERT INTO project_services_v2 (id, project_id, service_id, scraped_at, checked_at) VALUES (3, 2, 1, '2018-06-13 15:06:37', '2018-06-13 15:06:37');
INSERT INTO project_services_v2 (id, project_id, service_id, scraped_at, checked_at) VALUES (4, 2, 2, '2018-06-13 15:06:37', '2018-06-13 15:06:37');
INSERT INTO project_services_v2 (id, project_id, service_id, scraped_at, checked_at) VALUES (5, 3, 1, '2018-06-13 15:06:37', '2018-06-13 15:06:37');
INSERT INTO project_services_v2 (id, project_id, service_id, scraped_at, checked_at) VALUES (6, 3, 2, '2018-06-13 15:06:37', '2018-06-13 15:06:37');

-- project_resources contains some pathological cases
INSERT INTO project_resources_v2 (id, project_id, resource_id, quota, backend_quota) VALUES (1, 1, 1, 30,  10);
INSERT INTO project_resources_v2 (id, project_id, resource_id, quota, backend_quota) VALUES (2, 1, 2, 100, 100);
INSERT INTO project_resources_v2 (id, project_id, resource_id, quota, backend_quota) VALUES (3, 1, 3, 10,  10);
INSERT INTO project_resources_v2 (id, project_id, resource_id, quota, backend_quota) VALUES (4, 2, 1, 14,  14);
INSERT INTO project_resources_v2 (id, project_id, resource_id, quota, backend_quota) VALUES (5, 2, 2, 60,  60);
INSERT INTO project_resources_v2 (id, project_id, resource_id, quota, backend_quota) VALUES (6, 2, 3, 5,   5);
INSERT INTO project_resources_v2 (id, project_id, resource_id, quota, backend_quota) VALUES (7, 3, 1, 30,  30);
INSERT INTO project_resources_v2 (id, project_id, resource_id, quota, backend_quota) VALUES (8, 3, 2, 62,  62);
INSERT INTO project_resources_v2 (id, project_id, resource_id, quota, backend_quota) VALUES (9, 3, 3, 10,  10);

-- project_az_resources has everything as non-AZ-aware (the consistency checks do not really care about AZs)
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage, physical_usage) VALUES (1, 1, 1, 14, NULL);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage, physical_usage) VALUES (2, 1, 2, 88, 92);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage, physical_usage) VALUES (3, 1, 3, 5,  NULL);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage, physical_usage) VALUES (4, 2, 1, 18, NULL);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage, physical_usage) VALUES (5, 2, 2, 45, 40);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage, physical_usage) VALUES (6, 2, 3, 2,  NULL);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage, physical_usage) VALUES (7, 3, 1, 20, NULL);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage, physical_usage) VALUES (8, 3, 2, 48, 43);
INSERT INTO project_az_resources_v2 (id, project_id, az_resource_id, usage, physical_usage) VALUES (9, 3, 3, 4,  NULL);
