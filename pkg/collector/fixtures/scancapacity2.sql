INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (1, 'shared', 'shared', 0);
INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (2, 'west', 'unshared', 0);
INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (3, 'west', 'unknown', 1);

INSERT INTO cluster_resources (service_id, name, capacity, comment) VALUES (2, 'capacity', 42, '');
INSERT INTO cluster_resources (service_id, name, capacity, comment) VALUES (1, 'unknown', 100, '');
