-- BEGIN skip in sqlite
ALTER TABLE domains DROP CONSTRAINT domains_uuid_cluster_id_key;
ALTER TABLE domains ADD UNIQUE (uuid);
-- END skip in sqlite
