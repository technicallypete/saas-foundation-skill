-- apply-as: owner
-- Fixture schema (reference shape for the tenancy, RLS and audit foundation
-- stages; recommended defaults, not decisions). Applied by the migrator with
-- SET ROLE <db>_owner, so every object is owned by the NOLOGIN owner role.
SET ROLE :"owner_role";
SET search_path = app, pg_catalog;

-- Context helpers: transaction-local settings; empty or missing means NULL,
-- so comparisons match nothing (fail closed). Malformed values raise.
CREATE FUNCTION app.ctx_org() RETURNS uuid LANGUAGE sql STABLE
  AS $$ SELECT NULLIF(current_setting('app.organization_id', true), '')::uuid $$;
CREATE FUNCTION app.ctx_user() RETURNS uuid LANGUAGE sql STABLE
  AS $$ SELECT NULLIF(current_setting('app.user_id', true), '')::uuid $$;
CREATE FUNCTION app.ctx_group() RETURNS uuid LANGUAGE sql STABLE
  AS $$ SELECT NULLIF(current_setting('app.group_id', true), '')::uuid $$;

-- Global identity: not tenant-owned; visibility is still restricted by RLS.
CREATE TABLE app.identities (
  id           uuid PRIMARY KEY,
  email        text NOT NULL UNIQUE,
  display_name text NOT NULL
);

-- Organization: the tenant boundary. Slug is global (subdomain routing).
CREATE TABLE app.organizations (
  id   uuid PRIMARY KEY,
  slug text NOT NULL UNIQUE
       CONSTRAINT organizations_slug_format CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$' AND length(slug) <= 63)
       CONSTRAINT organizations_slug_reserved CHECK (slug NOT IN ('www', 'api', 'app', 'admin', 'auth', 'login', 'logout', 'static', 'assets', 'mcp', 'health', 'status')),
  name text NOT NULL
);

CREATE TABLE app.organization_policies (
  organization_id uuid PRIMARY KEY REFERENCES app.organizations (id),
  group_admins_edit_business_profiles boolean NOT NULL DEFAULT true
);

-- Group: Organization-owned collaboration scope. Immutable UUID, editable
-- kebab-case slug unique within its Organization. Slugs never grant access.
CREATE TABLE app.groups (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  organization_id uuid NOT NULL REFERENCES app.organizations (id),
  slug            text NOT NULL
                  CONSTRAINT groups_slug_format CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$' AND length(slug) <= 63)
                  CONSTRAINT groups_slug_reserved CHECK (slug NOT IN ('new', 'settings', 'admin', 'api', 'members', 'onboarding', 'billing', 'login', 'logout')),
  name            text NOT NULL,
  CONSTRAINT groups_org_slug_unique UNIQUE (organization_id, slug),
  CONSTRAINT groups_org_id_unique UNIQUE (organization_id, id)
);

-- BusinessEntity: reusable Organization-owned business context.
CREATE TABLE app.business_entities (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  organization_id uuid NOT NULL REFERENCES app.organizations (id),
  name            text NOT NULL,
  profile         jsonb NOT NULL DEFAULT '{}'::jsonb,
  CONSTRAINT business_entities_org_id_unique UNIQUE (organization_id, id)
);

-- GroupBusinessEntity: composite FKs make cross-Organization links impossible.
CREATE TABLE app.group_business_entities (
  organization_id    uuid NOT NULL REFERENCES app.organizations (id),
  group_id           uuid NOT NULL,
  business_entity_id uuid NOT NULL,
  PRIMARY KEY (organization_id, group_id, business_entity_id),
  FOREIGN KEY (organization_id, group_id) REFERENCES app.groups (organization_id, id) ON DELETE CASCADE,
  FOREIGN KEY (organization_id, business_entity_id) REFERENCES app.business_entities (organization_id, id) ON DELETE CASCADE
);

-- RoleBinding: the ONLY mutable store of roles. Membership entities are
-- read-only projections of it (views below). Typed scope columns carry
-- composite same-Organization FKs; the CHECK keeps them consistent with the
-- canonical (scope_type, scope_id) columns.
CREATE TABLE app.role_bindings (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  organization_id     uuid NOT NULL REFERENCES app.organizations (id),
  subject_identity_id uuid NOT NULL REFERENCES app.identities (id),
  scope_type          text NOT NULL CHECK (scope_type IN ('organization', 'group', 'business_entity')),
  scope_id            uuid NOT NULL,
  role                text NOT NULL,
  source              text NOT NULL CHECK (source IN ('membership', 'direct_grant')),
  group_id            uuid,
  business_entity_id  uuid,
  FOREIGN KEY (organization_id, group_id) REFERENCES app.groups (organization_id, id) ON DELETE CASCADE,
  FOREIGN KEY (organization_id, business_entity_id) REFERENCES app.business_entities (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT role_bindings_scope_consistent CHECK (
       (scope_type = 'organization' AND scope_id = organization_id AND group_id IS NULL AND business_entity_id IS NULL
        AND role IN ('org_owner', 'org_admin', 'org_member') AND source = 'membership')
    OR (scope_type = 'group' AND group_id = scope_id AND business_entity_id IS NULL
        AND role IN ('group_admin', 'group_member') AND source = 'membership')
    OR (scope_type = 'business_entity' AND business_entity_id = scope_id AND group_id IS NULL
        AND role IN ('be_owner', 'be_admin'))
  )
);
-- At most one membership role per subject and scope.
CREATE UNIQUE INDEX role_bindings_one_membership ON app.role_bindings (subject_identity_id, scope_type, scope_id) WHERE source = 'membership';
CREATE UNIQUE INDEX role_bindings_unique_grant ON app.role_bindings (subject_identity_id, scope_type, scope_id, role) WHERE source = 'direct_grant';

-- Canonical membership entities: derived, read-only projections.
CREATE VIEW app.organization_memberships WITH (security_invoker = true) AS
  SELECT subject_identity_id AS user_id, organization_id, role
  FROM app.role_bindings WHERE scope_type = 'organization' AND source = 'membership';
CREATE VIEW app.group_memberships WITH (security_invoker = true) AS
  SELECT subject_identity_id AS user_id, organization_id, group_id, role
  FROM app.role_bindings WHERE scope_type = 'group' AND source = 'membership';
CREATE VIEW app.business_entity_memberships WITH (security_invoker = true) AS
  SELECT subject_identity_id AS user_id, organization_id, business_entity_id, role, source
  FROM app.role_bindings WHERE scope_type = 'business_entity';

-- AuditEvent: controlled and attributed. Organization is nullable only for
-- security events that occur before any context exists.
CREATE TABLE app.audit_events (
  id              uuid PRIMARY KEY,
  occurred_at     timestamptz NOT NULL DEFAULT now(),
  actor_type      text NOT NULL CHECK (actor_type IN ('user', 'service', 'system')),
  actor_id        text NOT NULL,
  organization_id uuid REFERENCES app.organizations (id),
  group_id        uuid,
  resource_type   text NOT NULL,
  resource_id     text NOT NULL,
  action          text NOT NULL,
  outcome         text NOT NULL CHECK (outcome IN ('allowed', 'denied', 'error')),
  trace_id        text NOT NULL,
  request_id      text NOT NULL,
  detail          jsonb NOT NULL DEFAULT '{}'::jsonb
);

-- Object metadata: the authorization anchor for storage. Keys are
-- server-generated and opaque; prefixes confer nothing.
CREATE TABLE app.stored_objects (
  id              uuid PRIMARY KEY,
  organization_id uuid NOT NULL REFERENCES app.organizations (id),
  group_id        uuid,
  object_key      text NOT NULL UNIQUE
                  -- The key is derived from the row itself, so metadata can never point at
                  -- another Organization's object.
                  CONSTRAINT stored_objects_key_derived CHECK (object_key = 'org/' || organization_id::text || '/' || id::text),
  content_type    text NOT NULL,
  created_by      uuid NOT NULL REFERENCES app.identities (id),
  FOREIGN KEY (organization_id, group_id) REFERENCES app.groups (organization_id, id),
  CONSTRAINT stored_objects_org_id_unique UNIQUE (organization_id, id)
);

-- Immutability: id and organization_id never change.
CREATE FUNCTION app.forbid_identity_change() RETURNS trigger LANGUAGE plpgsql
  SET search_path = pg_catalog AS $$
BEGIN
  IF NEW.id IS DISTINCT FROM OLD.id THEN
    RAISE EXCEPTION '%.id is immutable', TG_TABLE_NAME USING ERRCODE = 'integrity_constraint_violation';
  END IF;
  IF TG_TABLE_NAME <> 'organizations' AND NEW.organization_id IS DISTINCT FROM OLD.organization_id THEN
    RAISE EXCEPTION '%.organization_id is immutable', TG_TABLE_NAME USING ERRCODE = 'integrity_constraint_violation';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER organizations_immutable BEFORE UPDATE ON app.organizations FOR EACH ROW EXECUTE FUNCTION app.forbid_identity_change();
CREATE TRIGGER groups_immutable BEFORE UPDATE ON app.groups FOR EACH ROW EXECUTE FUNCTION app.forbid_identity_change();
CREATE TRIGGER business_entities_immutable BEFORE UPDATE ON app.business_entities FOR EACH ROW EXECUTE FUNCTION app.forbid_identity_change();
CREATE TRIGGER role_bindings_immutable BEFORE UPDATE ON app.role_bindings FOR EACH ROW EXECUTE FUNCTION app.forbid_identity_change();
CREATE TRIGGER stored_objects_immutable BEFORE UPDATE ON app.stored_objects FOR EACH ROW EXECUTE FUNCTION app.forbid_identity_change();

-- Audit append-only: every UPDATE, DELETE and TRUNCATE is refused for every
-- role that fires the trigger, except DELETE by the retention role of rows
-- older than the window. Residual risk: the table owner (migration path) can
-- disable triggers; keep migrations reviewed (see references/storage-audit.md).
-- Privileges (30_policies.sql) are the first line; this trigger is the second.
-- The retention role name is embedded as a literal (psql \gexec), because a
-- non-superuser cannot attach custom settings to a function.
SELECT format($fn$
CREATE FUNCTION app.audit_guard() RETURNS trigger LANGUAGE plpgsql
  SET search_path = pg_catalog AS $body$
BEGIN
  IF TG_OP = 'DELETE'
     AND current_user::text = %L
     AND OLD.occurred_at < now() - make_interval(days => 400) THEN
    RETURN OLD;
  END IF;
  RAISE EXCEPTION 'audit_events is append-only (%% refused)', TG_OP USING ERRCODE = 'insufficient_privilege';
END $body$
$fn$, :'retention_role') \gexec
CREATE TRIGGER audit_events_no_update_delete BEFORE UPDATE OR DELETE ON app.audit_events FOR EACH ROW EXECUTE FUNCTION app.audit_guard();
CREATE TRIGGER audit_events_no_truncate BEFORE TRUNCATE ON app.audit_events FOR EACH STATEMENT EXECUTE FUNCTION app.audit_guard();

-- Grants: explicit and minimal. New tables stay unreachable until granted.
GRANT EXECUTE ON FUNCTION app.ctx_org(), app.ctx_user(), app.ctx_group() TO :"runtime_role", :"retention_role";
GRANT SELECT ON app.identities, app.organizations, app.organization_policies TO :"runtime_role";
GRANT SELECT, INSERT, UPDATE, DELETE ON app.groups, app.business_entities, app.group_business_entities, app.role_bindings, app.stored_objects TO :"runtime_role";
GRANT SELECT ON app.organization_memberships, app.group_memberships, app.business_entity_memberships TO :"runtime_role";
-- Column-level INSERT: occurred_at is always server time (its default); the
-- runtime role cannot backdate events into the retention window.
GRANT INSERT (id, actor_type, actor_id, organization_id, group_id, resource_type, resource_id, action, outcome, trace_id, request_id, detail) ON app.audit_events TO :"runtime_role";
-- Retention may find and delete expired events but cannot read their content.
GRANT SELECT (id, occurred_at), DELETE ON app.audit_events TO :"retention_role";

RESET ROLE;
