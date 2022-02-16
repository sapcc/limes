-- start data for inconsistencies test
-- "cloud" cluster has two domains
INSERT INTO domains (id, cluster_id, name, uuid) VALUES (1, 'cloud', 'germany',  'uuid-for-germany');
INSERT INTO domains (id, cluster_id, name, uuid) VALUES (2, 'cloud', 'pakistan', 'uuid-for-pakistan');

-- domain_services is fully populated (as ensured by the collector's consistency check)
INSERT INTO domain_services (id, domain_id, type) VALUES (1, 1, 'compute');
INSERT INTO domain_services (id, domain_id, type) VALUES (2, 1, 'network');
INSERT INTO domain_services (id, domain_id, type) VALUES (3, 2, 'compute');
INSERT INTO domain_services (id, domain_id, type) VALUES (4, 2, 'network');

-- domain_resources has a hole where no domain quota (pakistan: loadbalancers) has been set yet
INSERT INTO domain_resources (service_id, name, quota) VALUES (1, 'cores',         100);
INSERT INTO domain_resources (service_id, name, quota) VALUES (1, 'ram',           1000);
INSERT INTO domain_resources (service_id, name, quota) VALUES (2, 'loadbalancers', 20);
INSERT INTO domain_resources (service_id, name, quota) VALUES (3, 'cores',         30);
INSERT INTO domain_resources (service_id, name, quota) VALUES (3, 'ram',           250);

-- "germany" has one project, and "pakistan" has two (lahore is a child project of karachi in order to check
-- correct rendering of the parent_uuid field)
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (1, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-germany', FALSE);
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (2, 2, 'karachi', 'uuid-for-karachi', 'uuid-for-pakistan', FALSE);
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (3, 2, 'lahore',  'uuid-for-lahore',  'uuid-for-karachi', FALSE);

-- project_services is fully populated (as ensured by the collector's consistency check)
INSERT INTO project_services (id, project_id, type, scraped_at, checked_at) VALUES (1, 1, 'compute', '2018-06-13 15:06:37', '2018-06-13 15:06:37');
INSERT INTO project_services (id, project_id, type, scraped_at, checked_at) VALUES (2, 1, 'network', '2018-06-13 15:06:37', '2018-06-13 15:06:37');
INSERT INTO project_services (id, project_id, type, scraped_at, checked_at) VALUES (3, 2, 'compute', '2018-06-13 15:06:37', '2018-06-13 15:06:37');
INSERT INTO project_services (id, project_id, type, scraped_at, checked_at) VALUES (4, 2, 'network', '2018-06-13 15:06:37', '2018-06-13 15:06:37');
INSERT INTO project_services (id, project_id, type, scraped_at, checked_at) VALUES (5, 3, 'compute', '2018-06-13 15:06:37', '2018-06-13 15:06:37');
INSERT INTO project_services (id, project_id, type, scraped_at, checked_at) VALUES (6, 3, 'network', '2018-06-13 15:06:37', '2018-06-13 15:06:37');

-- project_resources contains some pathological cases
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (1, 'cores',         30,  14, 10,  '', 30, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (1, 'ram',           100, 88, 100, '', 100, 92);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (2, 'loadbalancers', 10,  5,  10,  '', 10, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (3, 'cores',         14,  18, 14,  '', 14, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (3, 'ram',           60,  45, 60,  '', 60, 40);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (4, 'loadbalancers', 5,   2,  5,   '', 5, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (5, 'cores',         30,  20,  30,  '', 30, NULL);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (5, 'ram',           62,  48, 62,  '', 62, 43);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota, physical_usage) VALUES (6, 'loadbalancers', 10,  4,  10,  '', 10, NULL);
