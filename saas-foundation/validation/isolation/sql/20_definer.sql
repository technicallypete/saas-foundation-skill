-- apply-as: superuser
-- Narrowly scoped SECURITY DEFINER helpers for group-scope RLS. They read
-- only role_bindings and group_business_entities for the CURRENT context
-- (app.user_id, app.organization_id). Each pins search_path, is STABLE, is
-- owned by the NOLOGIN, NOBYPASSRLS <db>_policy_reader role, and is
-- executable by the runtime role only. The allowlist is asserted by tests.
-- The superuser is used here only to assign function ownership.

CREATE FUNCTION app.my_group_ids() RETURNS SETOF uuid
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
  SELECT rb.group_id FROM app.role_bindings rb
  WHERE rb.scope_type = 'group' AND rb.source = 'membership'
    AND rb.subject_identity_id = NULLIF(current_setting('app.user_id', true), '')::uuid
    AND rb.organization_id = NULLIF(current_setting('app.organization_id', true), '')::uuid
$$;

CREATE FUNCTION app.is_org_admin() RETURNS boolean
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
  SELECT EXISTS (
    SELECT 1 FROM app.role_bindings rb
    WHERE rb.scope_type = 'organization' AND rb.role IN ('org_owner', 'org_admin')
      AND rb.subject_identity_id = NULLIF(current_setting('app.user_id', true), '')::uuid
      AND rb.organization_id = NULLIF(current_setting('app.organization_id', true), '')::uuid)
$$;

CREATE FUNCTION app.my_business_entity_ids() RETURNS SETOF uuid
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
  SELECT gbe.business_entity_id FROM app.group_business_entities gbe
  JOIN app.role_bindings rb
    ON rb.scope_type = 'group' AND rb.source = 'membership'
   AND rb.group_id = gbe.group_id AND rb.organization_id = gbe.organization_id
  WHERE rb.subject_identity_id = NULLIF(current_setting('app.user_id', true), '')::uuid
    AND gbe.organization_id = NULLIF(current_setting('app.organization_id', true), '')::uuid
  UNION
  SELECT rb.business_entity_id FROM app.role_bindings rb
  WHERE rb.scope_type = 'business_entity'
    AND rb.subject_identity_id = NULLIF(current_setting('app.user_id', true), '')::uuid
    AND rb.organization_id = NULLIF(current_setting('app.organization_id', true), '')::uuid
$$;

CREATE FUNCTION app.my_organization_ids() RETURNS SETOF uuid
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
  SELECT DISTINCT rb.organization_id FROM app.role_bindings rb
  WHERE rb.subject_identity_id = NULLIF(current_setting('app.user_id', true), '')::uuid
$$;

ALTER FUNCTION app.my_group_ids() OWNER TO :"policy_reader_role";
ALTER FUNCTION app.is_org_admin() OWNER TO :"policy_reader_role";
ALTER FUNCTION app.my_business_entity_ids() OWNER TO :"policy_reader_role";
ALTER FUNCTION app.my_organization_ids() OWNER TO :"policy_reader_role";

REVOKE EXECUTE ON FUNCTION app.my_group_ids(), app.is_org_admin(), app.my_business_entity_ids(), app.my_organization_ids() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.my_group_ids(), app.is_org_admin(), app.my_business_entity_ids(), app.my_organization_ids() TO :"runtime_role";

GRANT SELECT ON app.role_bindings, app.group_business_entities TO :"policy_reader_role";
