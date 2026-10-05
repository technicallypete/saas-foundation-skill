#!/bin/sh
# Runs once, on first initialization of the data volume, as the image's init
# superuser. Creates separated roles; role names derive from DATABASE_NAME.
#   <db>_owner     NOLOGIN; owns schema "app" and every object in it
#   <db>_migrator  LOGIN; may SET ROLE <db>_owner (no inheritance) to migrate
#   <db>_runtime   LOGIN; application role: no superuser, no BYPASSRLS,
#                  no owner membership, so row-level security always applies
#   <db>_retention LOGIN; reserved for the audit-retention path
# New tables are unreachable to runtime until explicitly granted.
set -eu
db="${DATABASE_NAME:?DATABASE_NAME is required}"
: "${DB_MIGRATOR_PASSWORD:?}" "${DB_RUNTIME_PASSWORD:?}" "${DB_RETENTION_PASSWORD:?}"

psql -v ON_ERROR_STOP=1 --username "${POSTGRES_USER}" --dbname "${db}" \
  -v db="${db}" \
  -v owner_role="${db}_owner" -v migrator_role="${db}_migrator" \
  -v runtime_role="${db}_runtime" -v retention_role="${db}_retention" \
  <<'SQL'
-- Passwords are read from the environment (\getenv), never passed on the
-- psql command line.
\getenv migrator_pw DB_MIGRATOR_PASSWORD
\getenv runtime_pw DB_RUNTIME_PASSWORD
\getenv retention_pw DB_RETENTION_PASSWORD
CREATE ROLE :"owner_role" NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOINHERIT;
CREATE ROLE :"migrator_role" LOGIN PASSWORD :'migrator_pw' NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE;
GRANT :"owner_role" TO :"migrator_role" WITH INHERIT FALSE, SET TRUE;
CREATE ROLE :"runtime_role" LOGIN PASSWORD :'runtime_pw' NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOINHERIT;
CREATE ROLE :"retention_role" LOGIN PASSWORD :'retention_pw' NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOINHERIT;

-- Database: no PUBLIC access (CONNECT, TEMPORARY, CREATE); explicit CONNECT only.
REVOKE ALL ON DATABASE :"db" FROM PUBLIC;
GRANT CONNECT ON DATABASE :"db" TO :"migrator_role", :"runtime_role", :"retention_role";

-- Schemas: nothing may be created in public; application objects live in app.
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
CREATE SCHEMA app AUTHORIZATION :"owner_role";
REVOKE ALL ON SCHEMA app FROM PUBLIC;
GRANT USAGE ON SCHEMA app TO :"runtime_role", :"retention_role";

-- Defaults for objects the owner creates: functions are not executable by
-- PUBLIC; tables receive no grants until a migration grants them explicitly.
ALTER DEFAULT PRIVILEGES FOR ROLE :"owner_role" REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;
ALTER DEFAULT PRIVILEGES FOR ROLE :"owner_role" IN SCHEMA app GRANT USAGE, SELECT ON SEQUENCES TO :"runtime_role";

ALTER ROLE :"runtime_role" SET search_path = app;
ALTER ROLE :"retention_role" SET search_path = app;
ALTER ROLE :"migrator_role" SET search_path = app;
SQL
