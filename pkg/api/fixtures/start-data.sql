-- two services, one shared, one unshared
INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (1, 'west',   'unshared', 1000);
INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (2, 'shared', 'shared',   1100);
INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (3, 'east',   'unshared', 1200);

-- both services have the resources "things" and "capacity"; we can only scrape capacity for "things"...
INSERT INTO cluster_resources (service_id, name, capacity, comment, subcapacities) VALUES (1, 'things', 139, '', '[{"smaller_half":46},{"larger_half":93}]');
INSERT INTO cluster_resources (service_id, name, capacity, comment, subcapacities) VALUES (2, 'things', 246, '', '[{"smaller_half":82},{"larger_half":164}]');
INSERT INTO cluster_resources (service_id, name, capacity, comment, subcapacities) VALUES (3, 'things', 385, '', '[{"smaller_half":128},{"larger_half":257}]');
-- ...BUT we have manually-maintained capacity values for some of the "capacity" resources
INSERT INTO cluster_resources (service_id, name, capacity, comment, subcapacities) VALUES (2, 'capacity', 185, 'hand-counted', '');
INSERT INTO cluster_resources (service_id, name, capacity, comment, subcapacities) VALUES (3, 'capacity', 1000, 'rough estimate', '');

-- "west" has two domains, "east" has one domain
INSERT INTO domains (id, cluster_id, name, uuid) VALUES (1, 'west', 'germany', 'uuid-for-germany');
INSERT INTO domains (id, cluster_id, name, uuid) VALUES (2, 'west', 'france',  'uuid-for-france');
INSERT INTO domains (id, cluster_id, name, uuid) VALUES (3, 'east', 'poland',  'uuid-for-poland');

-- domain_services is fully populated (as ensured by the collector's consistency check)
INSERT INTO domain_services (id, domain_id, type) VALUES (1, 1, 'unshared');
INSERT INTO domain_services (id, domain_id, type) VALUES (2, 1, 'shared');
INSERT INTO domain_services (id, domain_id, type) VALUES (3, 2, 'unshared');
INSERT INTO domain_services (id, domain_id, type) VALUES (4, 2, 'shared');
INSERT INTO domain_services (id, domain_id, type) VALUES (5, 3, 'unshared');
INSERT INTO domain_services (id, domain_id, type) VALUES (6, 3, 'shared');

-- domain_resources has some holes where no domain quotas have been set yet
INSERT INTO domain_resources (service_id, name, quota) VALUES (1, 'things',   50);
INSERT INTO domain_resources (service_id, name, quota) VALUES (1, 'capacity', 45);
INSERT INTO domain_resources (service_id, name, quota) VALUES (2, 'things',   30);
INSERT INTO domain_resources (service_id, name, quota) VALUES (2, 'capacity', 25);
INSERT INTO domain_resources (service_id, name, quota) VALUES (3, 'things',   20);
INSERT INTO domain_resources (service_id, name, quota) VALUES (3, 'capacity', 55);
INSERT INTO domain_resources (service_id, name, quota) VALUES (5, 'things',   10);
INSERT INTO domain_resources (service_id, name, quota) VALUES (5, 'capacity', 15);
INSERT INTO domain_resources (service_id, name, quota) VALUES (6, 'things',   60);
INSERT INTO domain_resources (service_id, name, quota) VALUES (6, 'capacity', 25);

-- "germany" has two projects, the other domains have one each (Dresden is a child project of Berlin in order to check
-- correct rendering of the parent_uuid field)
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (3, 2, 'paris', 'uuid-for-paris', 'uuid-for-france');
INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (4, 3, 'warsaw', 'uuid-for-warsaw', 'uuid-for-poland');

-- project_services is fully populated (as ensured by the collector's consistency check)
INSERT INTO project_services (id, project_id, type, scraped_at) VALUES (1, 1, 'unshared', 11);
INSERT INTO project_services (id, project_id, type, scraped_at) VALUES (2, 1, 'shared',   22);
INSERT INTO project_services (id, project_id, type, scraped_at) VALUES (3, 2, 'unshared', 33);
INSERT INTO project_services (id, project_id, type, scraped_at) VALUES (4, 2, 'shared',   44);
INSERT INTO project_services (id, project_id, type, scraped_at) VALUES (5, 3, 'unshared', 55);
INSERT INTO project_services (id, project_id, type, scraped_at) VALUES (6, 3, 'shared',   66);
INSERT INTO project_services (id, project_id, type, scraped_at) VALUES (7, 4, 'unshared', 77);
INSERT INTO project_services (id, project_id, type, scraped_at) VALUES (8, 4, 'shared',   88);

-- project_resources contains some pathological cases
-- berlin (also used for test cases concerning subresources)
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources) VALUES (1, 'things',   10, 2, 10, '[{"id":"firstthing","value":23},{"id":"secondthing","value":42}]');
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources) VALUES (1, 'capacity', 10, 2, 10, '');
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources) VALUES (2, 'things',   10, 2, 10, '[{"id":"thirdthing","value":5},{"id":"fourththing","value":123}]');
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources) VALUES (2, 'capacity', 10, 2, 10, '');
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources) VALUES (2, 'external_things', 1, 0, 1, '');
-- dresden (backend quota for shared/capacity mismatches approved quota and exceeds domain quota)
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources) VALUES (3, 'things',   10, 2, 10, '');
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources) VALUES (3, 'capacity', 10, 2, 10, '');
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources) VALUES (4, 'things',   10, 2, 10, '');
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources) VALUES (4, 'capacity', 10, 2, 100, '');
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources) VALUES (4, 'external_things', 1, 0, 1, '');
-- paris (infinite backend quota for unshared/things)
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources) VALUES (5, 'things',   10, 2, -1, '');
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources) VALUES (5, 'capacity', 10, 2, 10, '');
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources) VALUES (6, 'things',   10, 2, 10, '');
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources) VALUES (6, 'capacity', 10, 2, 10, '');
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources) VALUES (6, 'external_things', 1, 0, 1, '');
-- warsaw
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources) VALUES (7, 'things',   10, 2, 10, '');
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources) VALUES (7, 'capacity', 10, 2, 10, '');
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources) VALUES (8, 'things',   10, 2, 10, '');
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources) VALUES (8, 'capacity', 10, 2, 10, '');
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources) VALUES (8, 'external_things', 1, 0, 1, '');

-- insert some bullshit data that should be filtered out by the pkg/reports/ logic
-- (cluster "north", service "weird" and resource "items" are not configured)
INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (101, 'north', 'unshared', 1000);
INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (102, 'north', 'shared',   1100);
INSERT INTO cluster_resources (service_id, name, capacity) VALUES (101, 'things', 1);
INSERT INTO cluster_resources (service_id, name, capacity) VALUES (102, 'things', 1);

INSERT INTO domain_services (id, domain_id, type) VALUES (101, 1, 'weird');
INSERT INTO domain_resources (service_id, name, quota) VALUES (101, 'things', 1);
INSERT INTO project_services (id, project_id, type) VALUES (101, 1, 'weird');
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources) VALUES (101, 'things', 2, 1, 2, '');

INSERT INTO domain_resources (service_id, name, quota) VALUES (1, 'items', 1);
INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources) VALUES (1, 'items', 2, 1, 2, '');
