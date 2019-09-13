INSERT INTO cluster_resources (service_id, name, capacity, comment, subcapacities, capacity_per_az) VALUES (1, 'capacity', 42, '', '', '');
INSERT INTO cluster_resources (service_id, name, capacity, comment, subcapacities, capacity_per_az) VALUES (1, 'things', 23, '', '', '');
INSERT INTO cluster_resources (service_id, name, capacity, comment, subcapacities, capacity_per_az) VALUES (2, 'capacity', 42, '', '', '');
INSERT INTO cluster_resources (service_id, name, capacity, comment, subcapacities, capacity_per_az) VALUES (2, 'things', 42, '', '[{"smaller_half":14},{"larger_half":28}]', '');
INSERT INTO cluster_resources (service_id, name, capacity, comment, subcapacities, capacity_per_az) VALUES (3, 'capacity', 50, 'manual', '', '');

INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (1, 'shared', 'shared', 4);
INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (2, 'west', 'unshared', 4);
INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (3, 'west', 'unshared2', 4);
