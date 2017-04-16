INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (1, 'shared', 'shared', 2);
INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (2, 'west', 'unshared', 2);

INSERT INTO cluster_resources (service_id, name, capacity, comment) VALUES (2, 'capacity', 42, '');
INSERT INTO cluster_resources (service_id, name, capacity, comment) VALUES (1, 'things', 23, '');
