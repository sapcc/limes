-- BEGIN skip in sqlite
ALTER TABLE domains DROP CONSTRAINT domains_uuid_key;
ALTER TABLE domains ADD UNIQUE (uuid, cluster_id);
-- END skip in sqlite
