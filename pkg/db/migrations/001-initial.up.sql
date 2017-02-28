---------- cluster level

CREATE TABLE cluster_services (
  id         BIGSERIAL NOT NULL PRIMARY KEY,
  cluster_id TEXT,
  name       TEXT      NOT NULL,
  scraped_at TIMESTAMP NOT NULL DEFAULT to_timestamp(0),
  UNIQUE (cluster_id, name),
);

CREATE TABLE cluster_resources (
  service_id BIGINT NOT NULL REFERENCES cluster_services ON DELETE CASCADE,
  name       TEXT   NOT NULL,
  capacity   BIGINT NOT NULL,
  PRIMARY KEY (service_id, name),
);

---------- domain level

CREATE TABLE domains (
  id         BIGSERIAL NOT NULL PRIMARY KEY,
  cluster_id TEXT      NOT NULL,
  name       TEXT      NOT NULL,
  uuid       TEXT      NOT NULL UNIQUE,
);

CREATE TABLE domain_services (
  id         BIGSERIAL NOT NULL PRIMARY KEY,
  domain_id  BIGINT    NOT NULL REFERENCES domains ON DELETE CASCADE,
  name       TEXT      NOT NULL,
  scraped_at TIMESTAMP NOT NULL DEFAULT to_timestamp(0),
  UNIQUE (domain_id, name),
);

CREATE TABLE domain_resources (
  service_id BIGINT NOT NULL REFERENCES domain_services ON DELETE CASCADE,
  name       TEXT   NOT NULL,
  quota      BIGINT NOT NULL,
  PRIMARY KEY (service_id, name),
);

---------- project level

CREATE TABLE projects (
  id        BIGSERIAL NOT NULL PRIMARY KEY,
  domain_id BIGINT    NOT NULL REFERENCES domains ON DELETE CASCADE,
  name      TEXT      NOT NULL,
  uuid      TEXT      NOT NULL UNIQUE,
);

CREATE TABLE project_services (
  id          BIGSERIAL NOT NULL PRIMARY KEY,
  project_id  BIGINT    NOT NULL REFERENCES projects ON DELETE CASCADE,
  name        TEXT      NOT NULL,
  scraped_at  TIMESTAMP NOT NULL DEFAULT to_timestamp(0),
  UNIQUE (project_id, name),
);

CREATE TABLE project_resources (
  service_id    BIGINT NOT NULL REFERENCES project_services ON DELETE CASCADE,
  name          TEXT   NOT NULL,
  quota         BIGINT NOT NULL,
  usage         BIGINT NOT NULL,
  backend_quota BIGINT NOT NULL,
  PRIMARY KEY (service_id, name),
);
