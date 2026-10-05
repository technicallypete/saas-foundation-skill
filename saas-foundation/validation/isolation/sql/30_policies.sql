-- apply-as: owner
-- Row-level security: ENABLE + FORCE on every table (FORCE makes the owner
-- path subject to RLS too). Separate per-command policies per role.
--   runtime: Organization boundary from transaction context plus group-scope
--            visibility through the narrowly scoped definer helpers.
--   owner:   Organization boundary only (no helper calls), so FORCE RLS
--            applies to migrations and maintenance on the owner path.
--   policy_reader / retention: narrowly scoped, role-specific policies.
SET ROLE :"owner_role";

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['identities', 'organizations', 'organization_policies', 'groups', 'business_entities',
                           'group_business_entities', 'role_bindings', 'audit_events', 'stored_objects'] LOOP
    EXECUTE format('ALTER TABLE app.%I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE app.%I FORCE ROW LEVEL SECURITY', t);
  END LOOP;
END $$;

-- identities: yourself, plus identities whose bindings you can see.
CREATE POLICY identities_runtime_select ON app.identities FOR SELECT TO :"runtime_role"
  USING (id = app.ctx_user() OR id IN (SELECT rb.subject_identity_id FROM app.role_bindings rb));
CREATE POLICY identities_owner_all ON app.identities FOR ALL TO :"owner_role" USING (true) WITH CHECK (true);

-- organizations: the active Organization, plus Organizations you belong to
-- (the switcher list), never others.
CREATE POLICY organizations_runtime_select ON app.organizations FOR SELECT TO :"runtime_role"
  USING (id = app.ctx_org() OR id IN (SELECT app.my_organization_ids()));
CREATE POLICY organizations_owner_all ON app.organizations FOR ALL TO :"owner_role"
  USING (id = app.ctx_org()) WITH CHECK (id = app.ctx_org());

CREATE POLICY organization_policies_runtime_select ON app.organization_policies FOR SELECT TO :"runtime_role"
  USING (organization_id = app.ctx_org());
CREATE POLICY organization_policies_owner_all ON app.organization_policies FOR ALL TO :"owner_role"
  USING (organization_id = app.ctx_org()) WITH CHECK (organization_id = app.ctx_org());

-- groups: Organization admins see every Group; others see their own Groups.
CREATE POLICY groups_runtime_select ON app.groups FOR SELECT TO :"runtime_role"
  USING (organization_id = app.ctx_org() AND (app.is_org_admin() OR id IN (SELECT app.my_group_ids())));
CREATE POLICY groups_runtime_insert ON app.groups FOR INSERT TO :"runtime_role"
  WITH CHECK (organization_id = app.ctx_org());
CREATE POLICY groups_runtime_update ON app.groups FOR UPDATE TO :"runtime_role"
  USING (organization_id = app.ctx_org() AND (app.is_org_admin() OR id IN (SELECT app.my_group_ids())))
  WITH CHECK (organization_id = app.ctx_org());
CREATE POLICY groups_runtime_delete ON app.groups FOR DELETE TO :"runtime_role"
  USING (organization_id = app.ctx_org() AND app.is_org_admin());
CREATE POLICY groups_owner_all ON app.groups FOR ALL TO :"owner_role"
  USING (organization_id = app.ctx_org()) WITH CHECK (organization_id = app.ctx_org());

-- business_entities: Organization admins, members of an associated Group,
-- and direct grant holders.
CREATE POLICY business_entities_runtime_select ON app.business_entities FOR SELECT TO :"runtime_role"
  USING (organization_id = app.ctx_org() AND (app.is_org_admin() OR id IN (SELECT app.my_business_entity_ids())));
CREATE POLICY business_entities_runtime_insert ON app.business_entities FOR INSERT TO :"runtime_role"
  WITH CHECK (organization_id = app.ctx_org());
CREATE POLICY business_entities_runtime_update ON app.business_entities FOR UPDATE TO :"runtime_role"
  USING (organization_id = app.ctx_org() AND (app.is_org_admin() OR id IN (SELECT app.my_business_entity_ids())))
  WITH CHECK (organization_id = app.ctx_org());
CREATE POLICY business_entities_runtime_delete ON app.business_entities FOR DELETE TO :"runtime_role"
  USING (organization_id = app.ctx_org() AND app.is_org_admin());
CREATE POLICY business_entities_owner_all ON app.business_entities FOR ALL TO :"owner_role"
  USING (organization_id = app.ctx_org()) WITH CHECK (organization_id = app.ctx_org());

CREATE POLICY group_business_entities_runtime_select ON app.group_business_entities FOR SELECT TO :"runtime_role"
  USING (organization_id = app.ctx_org() AND (app.is_org_admin() OR group_id IN (SELECT app.my_group_ids())));
CREATE POLICY group_business_entities_runtime_insert ON app.group_business_entities FOR INSERT TO :"runtime_role"
  WITH CHECK (organization_id = app.ctx_org());
CREATE POLICY group_business_entities_runtime_delete ON app.group_business_entities FOR DELETE TO :"runtime_role"
  USING (organization_id = app.ctx_org());
CREATE POLICY group_business_entities_owner_all ON app.group_business_entities FOR ALL TO :"owner_role"
  USING (organization_id = app.ctx_org()) WITH CHECK (organization_id = app.ctx_org());
CREATE POLICY group_business_entities_policy_reader ON app.group_business_entities FOR SELECT TO :"policy_reader_role"
  USING (true);

-- role_bindings: your own bindings in any Organization (switcher list);
-- within the active Organization, admins see all and Group members see the
-- bindings of their own Groups. Writes are pinned to the active Organization.
CREATE POLICY role_bindings_runtime_select ON app.role_bindings FOR SELECT TO :"runtime_role"
  USING (subject_identity_id = app.ctx_user()
         OR (organization_id = app.ctx_org()
             AND (app.is_org_admin() OR (scope_type = 'group' AND group_id IN (SELECT app.my_group_ids())))));
CREATE POLICY role_bindings_runtime_insert ON app.role_bindings FOR INSERT TO :"runtime_role"
  WITH CHECK (organization_id = app.ctx_org());
CREATE POLICY role_bindings_runtime_update ON app.role_bindings FOR UPDATE TO :"runtime_role"
  USING (organization_id = app.ctx_org()) WITH CHECK (organization_id = app.ctx_org());
CREATE POLICY role_bindings_runtime_delete ON app.role_bindings FOR DELETE TO :"runtime_role"
  USING (organization_id = app.ctx_org());
CREATE POLICY role_bindings_owner_all ON app.role_bindings FOR ALL TO :"owner_role"
  USING (organization_id = app.ctx_org()) WITH CHECK (organization_id = app.ctx_org());
CREATE POLICY role_bindings_policy_reader ON app.role_bindings FOR SELECT TO :"policy_reader_role"
  USING (true);

-- audit_events: runtime may only insert events for the active Organization
-- (or pre-context events with no Organization). The owner path is
-- Organization-bounded and still blocked from modification by the trigger.
-- Retention sees and deletes only rows older than the retention window.
CREATE POLICY audit_events_runtime_insert ON app.audit_events FOR INSERT TO :"runtime_role"
  WITH CHECK (organization_id IS NULL OR organization_id = app.ctx_org());
CREATE POLICY audit_events_owner_all ON app.audit_events FOR ALL TO :"owner_role"
  USING (organization_id = app.ctx_org()) WITH CHECK (organization_id = app.ctx_org());
CREATE POLICY audit_events_retention_select ON app.audit_events FOR SELECT TO :"retention_role"
  USING (occurred_at < now() - make_interval(days => 400));
CREATE POLICY audit_events_retention_delete ON app.audit_events FOR DELETE TO :"retention_role"
  USING (occurred_at < now() - make_interval(days => 400));

-- stored_objects: Organization-bounded; Group-scoped objects visible to
-- Organization admins and members of that Group.
CREATE POLICY stored_objects_runtime_select ON app.stored_objects FOR SELECT TO :"runtime_role"
  USING (organization_id = app.ctx_org() AND (group_id IS NULL OR app.is_org_admin() OR group_id IN (SELECT app.my_group_ids())));
CREATE POLICY stored_objects_runtime_insert ON app.stored_objects FOR INSERT TO :"runtime_role"
  WITH CHECK (organization_id = app.ctx_org());
CREATE POLICY stored_objects_runtime_delete ON app.stored_objects FOR DELETE TO :"runtime_role"
  USING (organization_id = app.ctx_org() AND (app.is_org_admin() OR group_id IN (SELECT app.my_group_ids())));
CREATE POLICY stored_objects_owner_all ON app.stored_objects FOR ALL TO :"owner_role"
  USING (organization_id = app.ctx_org()) WITH CHECK (organization_id = app.ctx_org());

RESET ROLE;
