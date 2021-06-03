INSERT INTO cluster_capacitors (cluster_id, capacitor_id, scraped_at, scrape_duration_secs, serialized_metrics) VALUES ('west', 'unittest', 5, 1, '');
INSERT INTO cluster_capacitors (cluster_id, capacitor_id, scraped_at, scrape_duration_secs, serialized_metrics) VALUES ('west', 'unittest2', 5, 1, '');

INSERT INTO cluster_resources (service_id, name, capacity, subcapacities, capacity_per_az) VALUES (1, 'things', 23, '', '');
INSERT INTO cluster_resources (service_id, name, capacity, subcapacities, capacity_per_az) VALUES (2, 'capacity', 42, '', '');

INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (1, 'shared', 'shared', 5);
INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (2, 'west', 'unshared', 5);
