INSERT INTO cluster_capacitors (cluster_id, capacitor_id, scraped_at, scrape_duration_secs, serialized_metrics) VALUES ('west', 'unittest', 24, 1, '');
INSERT INTO cluster_capacitors (cluster_id, capacitor_id, scraped_at, scrape_duration_secs, serialized_metrics) VALUES ('west', 'unittest2', 24, 1, '');
INSERT INTO cluster_capacitors (cluster_id, capacitor_id, scraped_at, scrape_duration_secs, serialized_metrics) VALUES ('west', 'unittest4', 24, 1, '{"smaller_half":3,"larger_half":7}');
INSERT INTO cluster_capacitors (cluster_id, capacitor_id, scraped_at, scrape_duration_secs, serialized_metrics) VALUES ('west', 'unittest5', 24, 1, '');

INSERT INTO cluster_resources (service_id, name, capacity, subcapacities, capacity_per_az) VALUES (1, 'things', 23, '', '');
INSERT INTO cluster_resources (service_id, name, capacity, subcapacities, capacity_per_az) VALUES (2, 'capacity', 42, '', '');
INSERT INTO cluster_resources (service_id, name, capacity, subcapacities, capacity_per_az) VALUES (2, 'things', 10, '[{"smaller_half":3},{"larger_half":7}]', '');
INSERT INTO cluster_resources (service_id, name, capacity, subcapacities, capacity_per_az) VALUES (3, 'things', 42, '', '[{"name":"az-one","capacity":21,"usage":4},{"name":"az-two","capacity":21,"usage":4}]');

INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (1, 'west', 'shared', 24);
INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (2, 'west', 'unshared', 24);
INSERT INTO cluster_services (id, cluster_id, type, scraped_at) VALUES (3, 'west', 'unshared2', 24);
