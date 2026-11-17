#!/usr/bin/env bash
# Runs only on first initialization of this disposable PostgreSQL instance.
set -euo pipefail
set +x
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<'SQL'
\getenv manager_password RUNAWAYBAO_DB_MANAGER_PASSWORD
REVOKE CONNECT, TEMPORARY ON DATABASE runawaybao FROM PUBLIC;
REVOKE CONNECT, TEMPORARY ON DATABASE postgres FROM PUBLIC;
REVOKE CONNECT, TEMPORARY ON DATABASE template1 FROM PUBLIC;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
CREATE ROLE runawaybao_reader NOLOGIN;
CREATE ROLE runawaybao_manager LOGIN PASSWORD :'manager_password' CREATEROLE NOINHERIT NOSUPERUSER NOCREATEDB NOREPLICATION NOBYPASSRLS;
GRANT runawaybao_reader TO runawaybao_manager WITH ADMIN OPTION;
GRANT CONNECT ON DATABASE runawaybao TO runawaybao_reader, runawaybao_manager;
CREATE SCHEMA demo;
REVOKE ALL ON SCHEMA demo FROM PUBLIC;
CREATE TABLE demo.fixture (id integer PRIMARY KEY, value text NOT NULL);
INSERT INTO demo.fixture VALUES (1, 'demo-value');
CREATE TABLE demo.restricted (id integer PRIMARY KEY);
GRANT USAGE ON SCHEMA demo TO runawaybao_reader;
GRANT SELECT ON demo.fixture TO runawaybao_reader;
SQL
