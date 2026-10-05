-- apply-as: superuser
-- Fixture-only role. The rendered infra/postgres/init/10-roles.sh has already
-- created <db>_owner, <db>_migrator, <db>_runtime and <db>_retention.
--
-- <db>_policy_reader owns the narrowly scoped SECURITY DEFINER helpers that
-- group-scope RLS needs to avoid policy recursion. It is NOLOGIN and
-- NOBYPASSRLS; it reads authority tables only through role-specific
-- SELECT-only policies (30_policies.sql).
CREATE ROLE :"policy_reader_role" NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOINHERIT;
GRANT USAGE ON SCHEMA app TO :"policy_reader_role";
