INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (1, 'shared', 'shared', 3);
INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (2, 'west', 'unshared', 3);
INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (4, 'west', 'unshared2', 3);

INSERT INTO cluster_resources (service_id, name, capacity, comment) VALUES (2, 'capacity', 42, '');
INSERT INTO cluster_resources (service_id, name, capacity, comment) VALUES (1, 'capacity', 42, '');
INSERT INTO cluster_resources (service_id, name, capacity, comment) VALUES (4, 'capacity', 50, 'manual');
INSERT INTO cluster_resources (service_id, name, capacity, comment) VALUES (1, 'things', 23, '');
