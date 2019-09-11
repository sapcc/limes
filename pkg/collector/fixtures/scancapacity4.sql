INSERT INTO cluster_resources (service_id, name, capacity, comment, capacity_per_az, subcapacities) VALUES (1, 'capacity', 42, '', '', '');
INSERT INTO cluster_resources (service_id, name, capacity, comment, capacity_per_az, subcapacities) VALUES (1, 'things', 23, '', '', '');
INSERT INTO cluster_resources (service_id, name, capacity, comment, capacity_per_az, subcapacities) VALUES (2, 'capacity', 42, '', '', '');
INSERT INTO cluster_resources (service_id, name, capacity, comment, capacity_per_az, subcapacities) VALUES (3, 'capacity', 50, 'manual', '', '');

INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (1, 'shared', 'shared', 3);
INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (2, 'west', 'unshared', 3);
INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (3, 'west', 'unshared2', 3);
