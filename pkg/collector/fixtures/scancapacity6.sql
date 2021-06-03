INSERT INTO cluster_capacitors (cluster_id, capacitor_id, scraped_at, scrape_duration_secs, serialized_metrics) VALUES ('west', 'unittest', 17, 1, '');
INSERT INTO cluster_capacitors (cluster_id, capacitor_id, scraped_at, scrape_duration_secs, serialized_metrics) VALUES ('west', 'unittest2', 17, 1, '');
INSERT INTO cluster_capacitors (cluster_id, capacitor_id, scraped_at, scrape_duration_secs, serialized_metrics) VALUES ('west', 'unittest4', 17, 1, '{"smaller_half":3,"larger_half":7}');

INSERT INTO cluster_resources (service_id, name, capacity, subcapacities, capacity_per_az) VALUES (1, 'things', 23, '', '');
INSERT INTO cluster_resources (service_id, name, capacity, subcapacities, capacity_per_az) VALUES (2, 'capacity', 42, '', '');
INSERT INTO cluster_resources (service_id, name, capacity, subcapacities, capacity_per_az) VALUES (2, 'things', 10, '[{"smaller_half":3},{"larger_half":7}]', '');

INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (1, 'shared', 'shared', 17);
INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (2, 'west', 'unshared', 17);
