INSERT INTO cluster_resources (service_id, name, capacity, comment, subcapacities, capacity_per_az) VALUES (1, 'capacity', 50, 'manual', '', '');
INSERT INTO cluster_resources (service_id, name, capacity, comment, subcapacities, capacity_per_az) VALUES (2, 'capacity', 42, '', '', '');
INSERT INTO cluster_resources (service_id, name, capacity, comment, subcapacities, capacity_per_az) VALUES (2, 'unknown', 100, '', '', '');
INSERT INTO cluster_resources (service_id, name, capacity, comment, subcapacities, capacity_per_az) VALUES (3, 'capacity', 50, 'manual', '', '');

INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (1, 'shared', 'shared', 0);
INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (2, 'west', 'unshared', 0);
INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (3, 'west', 'unshared2', 1);
