-- start data for inconsistencies test
-- "cloud" cluster has two domains
INSERT INTO domains (id, name, uuid) VALUES (1, 'germany',  'uuid-for-germany');
INSERT INTO domains (id, name, uuid) VALUES (2, 'pakistan', 'uuid-for-pakistan');

-- "germany" has one project, and "pakistan" has two (lahore is a child project of karachi in order to check
-- correct rendering of the parent_uuid field)
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-germany');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (2, 2, 'karachi', 'uuid-for-karachi', 'uuid-for-pakistan');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (3, 2, 'lahore',  'uuid-for-lahore',  'uuid-for-karachi');

-- project_services is fully populated (as ensured by the collector's consistency check)
INSERT INTO project_services (id, project_id, type, scraped_at, checked_at) VALUES (1, 1, 'compute', '2018-06-13 15:06:37', '2018-06-13 15:06:37');
INSERT INTO project_services (id, project_id, type, scraped_at, checked_at) VALUES (2, 1, 'network', '2018-06-13 15:06:37', '2018-06-13 15:06:37');
INSERT INTO project_services (id, project_id, type, scraped_at, checked_at) VALUES (3, 2, 'compute', '2018-06-13 15:06:37', '2018-06-13 15:06:37');
INSERT INTO project_services (id, project_id, type, scraped_at, checked_at) VALUES (4, 2, 'network', '2018-06-13 15:06:37', '2018-06-13 15:06:37');
INSERT INTO project_services (id, project_id, type, scraped_at, checked_at) VALUES (5, 3, 'compute', '2018-06-13 15:06:37', '2018-06-13 15:06:37');
INSERT INTO project_services (id, project_id, type, scraped_at, checked_at) VALUES (6, 3, 'network', '2018-06-13 15:06:37', '2018-06-13 15:06:37');

-- project_resources contains some pathological cases
INSERT INTO project_resources (id, service_id, name, quota, backend_quota) VALUES (1, 1, 'cores',         30,  10);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota) VALUES (2, 1, 'ram',           100, 100);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota) VALUES (3, 2, 'loadbalancers', 10,  10);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota) VALUES (4, 3, 'cores',         14,  14);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota) VALUES (5, 3, 'ram',           60,  60);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota) VALUES (6, 4, 'loadbalancers', 5,   5);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota) VALUES (7, 5, 'cores',         30,  30);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota) VALUES (8, 5, 'ram',           62,  62);
INSERT INTO project_resources (id, service_id, name, quota, backend_quota) VALUES (9, 6, 'loadbalancers', 10,  10);

-- project_az_resources has everything as non-AZ-aware (the consistency checks do not really care about AZs)
INSERT INTO project_az_resources (id, resource_id, az, usage, physical_usage) VALUES (1, 1, 'any', 14, NULL);
INSERT INTO project_az_resources (id, resource_id, az, usage, physical_usage) VALUES (2, 2, 'any', 88, 92);
INSERT INTO project_az_resources (id, resource_id, az, usage, physical_usage) VALUES (3, 3, 'any', 5,  NULL);
INSERT INTO project_az_resources (id, resource_id, az, usage, physical_usage) VALUES (4, 4, 'any', 18, NULL);
INSERT INTO project_az_resources (id, resource_id, az, usage, physical_usage) VALUES (5, 5, 'any', 45, 40);
INSERT INTO project_az_resources (id, resource_id, az, usage, physical_usage) VALUES (6, 6, 'any', 2,  NULL);
INSERT INTO project_az_resources (id, resource_id, az, usage, physical_usage) VALUES (7, 7, 'any', 20, NULL);
INSERT INTO project_az_resources (id, resource_id, az, usage, physical_usage) VALUES (8, 8, 'any', 48, 43);
INSERT INTO project_az_resources (id, resource_id, az, usage, physical_usage) VALUES (9, 9, 'any', 4,  NULL);
