-- apply-as: superuser
-- Synthetic seed data (setup only; never an assertion). Identifiers match
-- internal/fixture/seed.go. Superuser is used because FORCE RLS applies to
-- every other role.
INSERT INTO app.identities (id, email, display_name) VALUES
  ('c0000000-0000-4000-8000-000000000001', 'ada@example.test', 'Ada'),
  ('c0000000-0000-4000-8000-000000000002', 'lin@example.test', 'Lin'),
  ('c0000000-0000-4000-8000-000000000003', 'sam@example.test', 'Sam'),
  ('c0000000-0000-4000-8000-000000000004', 'kai@example.test', 'Kai'),
  ('c0000000-0000-4000-8000-000000000005', 'bea@example.test', 'Bea');

INSERT INTO app.organizations (id, slug, name) VALUES
  ('a0000000-0000-4000-8000-000000000001', 'alpha', 'Org Alpha'),
  ('b0000000-0000-4000-8000-000000000001', 'bravo', 'Org Bravo');

INSERT INTO app.organization_policies (organization_id, group_admins_edit_business_profiles) VALUES
  ('a0000000-0000-4000-8000-000000000001', true),
  ('b0000000-0000-4000-8000-000000000001', true);

INSERT INTO app.groups (id, organization_id, slug, name) VALUES
  ('a1000000-0000-4000-8000-000000000001', 'a0000000-0000-4000-8000-000000000001', 'ga-one', 'Group A One'),
  ('a1000000-0000-4000-8000-000000000002', 'a0000000-0000-4000-8000-000000000001', 'ga-two', 'Group A Two'),
  ('b1000000-0000-4000-8000-000000000001', 'b0000000-0000-4000-8000-000000000001', 'gb-one', 'Group B One');

INSERT INTO app.business_entities (id, organization_id, name, profile) VALUES
  ('a2000000-0000-4000-8000-000000000001', 'a0000000-0000-4000-8000-000000000001', 'Business A', '{"description": "shared by GA1 and GA2"}'),
  ('a2000000-0000-4000-8000-000000000002', 'a0000000-0000-4000-8000-000000000001', 'Business A2', '{"description": "GA2 only"}'),
  ('b2000000-0000-4000-8000-000000000001', 'b0000000-0000-4000-8000-000000000001', 'Business B', '{}');

INSERT INTO app.group_business_entities (organization_id, group_id, business_entity_id) VALUES
  ('a0000000-0000-4000-8000-000000000001', 'a1000000-0000-4000-8000-000000000001', 'a2000000-0000-4000-8000-000000000001'),
  ('a0000000-0000-4000-8000-000000000001', 'a1000000-0000-4000-8000-000000000002', 'a2000000-0000-4000-8000-000000000001'),
  ('a0000000-0000-4000-8000-000000000001', 'a1000000-0000-4000-8000-000000000002', 'a2000000-0000-4000-8000-000000000002'),
  ('b0000000-0000-4000-8000-000000000001', 'b1000000-0000-4000-8000-000000000001', 'b2000000-0000-4000-8000-000000000001');

-- Authority (the only role store).
INSERT INTO app.role_bindings (organization_id, subject_identity_id, scope_type, scope_id, role, source, group_id, business_entity_id) VALUES
  -- ada: alpha org_owner, bravo org_member
  ('a0000000-0000-4000-8000-000000000001', 'c0000000-0000-4000-8000-000000000001', 'organization', 'a0000000-0000-4000-8000-000000000001', 'org_owner', 'membership', NULL, NULL),
  ('b0000000-0000-4000-8000-000000000001', 'c0000000-0000-4000-8000-000000000001', 'organization', 'b0000000-0000-4000-8000-000000000001', 'org_member', 'membership', NULL, NULL),
  -- lin: GA1 group_admin only
  ('a0000000-0000-4000-8000-000000000001', 'c0000000-0000-4000-8000-000000000002', 'group', 'a1000000-0000-4000-8000-000000000001', 'group_admin', 'membership', 'a1000000-0000-4000-8000-000000000001', NULL),
  -- sam: GA2 group_member only
  ('a0000000-0000-4000-8000-000000000001', 'c0000000-0000-4000-8000-000000000003', 'group', 'a1000000-0000-4000-8000-000000000002', 'group_member', 'membership', 'a1000000-0000-4000-8000-000000000002', NULL),
  -- kai: direct be_owner grant on BE-A only
  ('a0000000-0000-4000-8000-000000000001', 'c0000000-0000-4000-8000-000000000004', 'business_entity', 'a2000000-0000-4000-8000-000000000001', 'be_owner', 'direct_grant', NULL, 'a2000000-0000-4000-8000-000000000001'),
  -- bea: bravo org_member only
  ('b0000000-0000-4000-8000-000000000001', 'c0000000-0000-4000-8000-000000000005', 'organization', 'b0000000-0000-4000-8000-000000000001', 'org_member', 'membership', NULL, NULL);

-- Audit: one expired and one recent event per Organization.
INSERT INTO app.audit_events (id, occurred_at, actor_type, actor_id, organization_id, group_id, resource_type, resource_id, action, outcome, trace_id, request_id) VALUES
  ('d0000000-0000-4000-8000-000000000001', now() - interval '500 days', 'user', 'c0000000-0000-4000-8000-000000000001', 'a0000000-0000-4000-8000-000000000001', NULL, 'group', 'a1000000-0000-4000-8000-000000000001', 'group.update', 'allowed', 'seed-trace-1', 'seed-req-1'),
  ('d0000000-0000-4000-8000-000000000002', now() - interval '1 day', 'user', 'c0000000-0000-4000-8000-000000000001', 'a0000000-0000-4000-8000-000000000001', NULL, 'group', 'a1000000-0000-4000-8000-000000000001', 'group.update', 'allowed', 'seed-trace-2', 'seed-req-2'),
  ('d0000000-0000-4000-8000-000000000003', now() - interval '500 days', 'user', 'c0000000-0000-4000-8000-000000000001', 'b0000000-0000-4000-8000-000000000001', NULL, 'group', 'b1000000-0000-4000-8000-000000000001', 'group.read', 'allowed', 'seed-trace-3', 'seed-req-3'),
  ('d0000000-0000-4000-8000-000000000004', now() - interval '1 day', 'user', 'c0000000-0000-4000-8000-000000000001', 'b0000000-0000-4000-8000-000000000001', NULL, 'group', 'b1000000-0000-4000-8000-000000000001', 'group.read', 'denied', 'seed-trace-4', 'seed-req-4');

-- Object metadata. Keys are opaque; the alpha object lives under a
-- Group-scoped key, the bravo object under its own Organization prefix.
INSERT INTO app.stored_objects (id, organization_id, group_id, object_key, content_type, created_by) VALUES
  ('a3000000-0000-4000-8000-000000000001', 'a0000000-0000-4000-8000-000000000001', 'a1000000-0000-4000-8000-000000000001', 'org/a0000000-0000-4000-8000-000000000001/a3000000-0000-4000-8000-000000000001', 'text/plain', 'c0000000-0000-4000-8000-000000000001'),
  ('b3000000-0000-4000-8000-000000000001', 'b0000000-0000-4000-8000-000000000001', 'b1000000-0000-4000-8000-000000000001', 'org/b0000000-0000-4000-8000-000000000001/b3000000-0000-4000-8000-000000000001', 'text/plain', 'c0000000-0000-4000-8000-000000000001');
