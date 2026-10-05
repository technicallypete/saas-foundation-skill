# Tenancy and authorization

## Identity

- One **global identity** per person; it may belong to many Organizations with different roles. Authority is never global and never inferred across Organizations.
- Integrate an external identity provider behind an owned `IdentityProvider` interface; do **not** build a custom authentication provider. Provide session lifecycle, logout and revocation, and MFA/SSO capability (STAGE-IDENTITY).
- The identity table is global but still protected by row-level security: a caller sees itself and identities that share a scope it can see.

## Canonical model

```text
Global Identity
  -> Organization                 tenant/security boundary; organization_id on every tenant-owned row
       -> Group                   collaboration scope; immutable UUID + editable kebab-case slug
       -> BusinessEntity          reusable business context ("Business Profile" in generic UI)
            -> BillingProfile[]   one BusinessEntity may own several (not implemented by the archetype)
GroupBusinessEntity               many-to-many association inside one Organization
RoleBinding(subject, scope_type, scope_id, role, source)   the single mutable authority store
```

- `TenantContext` is a trusted, server-resolved security envelope (identity, active Organization, optional Group); it is never a persisted tenant table.
- **Groups:** `UNIQUE (organization_id, slug)`, reserved-route validation, slug suggested from the name and editable; the UUID never changes. Slugs and hostnames are routing selectors and never grant access.
- **BusinessEntity** is not a child of one Group; it is associated with many Groups. Same-Organization associations are enforced by composite foreign keys **and** by service validation before any write.
- **Billing extensibility:** keep BusinessEntity → many BillingProfiles possible; billing, payment, tax and legal operations are sensitive capabilities that require direct authority. No billing schema is part of the foundation.

## Memberships and RoleBinding

The canonical membership entities keep their shape and role — `OrganizationMembership(user_id, organization_id, role)`, `GroupMembership(user_id, group_id, role)`, `BusinessEntityMembership(user_id, business_entity_id, role)` — and are **read-only projections** of RoleBinding:

- `role_bindings` is the only place a role can be written. `source` records how it arose (`membership`, `direct_grant`; later stages may add `invitation`, `system`). At most one membership role per subject and scope.
- Granting, changing or removing a membership role is one write to one row; the membership projection, row-level visibility and inherited capabilities change with it. No BusinessEntity row mirrors a Group grant.
- Membership lifecycle attributes, if needed, live in a separate table keyed by the binding and carry no role.

## Policy evaluation

- Deny by default. Effective capabilities are computed server-side at evaluation time from: Organization bindings, Group bindings (own Group only), direct BusinessEntity grants, capability-specific inheritance from associated Groups, and Organization policy.
- Inheritance is capability-specific: a Group administrator may edit ordinary Business Profile data of an associated BusinessEntity (while Organization policy allows it) but never gains ownership, privileged settings, policy, audit or role-grant authority.
- Client- and model-supplied identifiers are selectors: a foreign or unknown identifier fails exactly like a nonexistent one.
- Every execution rechecks authority; cached catalogs or sessions never substitute for evaluation.

## Permission matrix

Deny by default. Capabilities derive from RoleBinding rows at evaluation time; nothing is copied. Cells:

- `✓`: allowed through a binding at that scope.
- `✓ all`: allowed for every Group in the Organization.
- `✓ own`: allowed only for Groups where the role is held.
- `✓ via Group`: inherited through a GroupBusinessEntity association.
- `✓ via Group if policy`: inherited only while Organization policy `group_admins_edit_business_profiles` is on (the default).
- `—`: denied.

Sensitive capabilities (ownership, privileged settings, policy, audit, role grants) are never inherited through a Group.

<!-- permission-matrix:begin -->
| Capability | org_owner | org_admin | org_member | group_admin | group_member | be_owner | be_admin |
|---|---|---|---|---|---|---|---|
| organization.read | ✓ | ✓ | ✓ | — | — | — | — |
| organization.manage_members | ✓ | ✓ | — | — | — | — | — |
| organization.manage_policy | ✓ | — | — | — | — | — | — |
| group.read | ✓ all | ✓ all | — | ✓ own | ✓ own | — | — |
| group.update | ✓ all | ✓ all | — | ✓ own | — | — | — |
| group.manage_members | ✓ all | ✓ all | — | ✓ own | — | — | — |
| business_entity.profile.read | ✓ | ✓ | — | ✓ via Group | ✓ via Group | ✓ | ✓ |
| business_entity.profile.update | ✓ | ✓ | — | ✓ via Group if policy | — | ✓ | ✓ |
| business_entity.select_for_onboarding | ✓ | ✓ | — | — | — | ✓ | ✓ |
| business_entity.ownership.transfer | ✓ | — | — | — | — | ✓ | — |
| business_entity.privileged_settings.manage | ✓ | — | — | — | — | ✓ | ✓ |
| audit.read | ✓ | — | — | — | — | — | — |
<!-- permission-matrix:end -->

`storage.object.read` is not a role column: it resolves to the read capability of the object's owning scope (Group or Organization) for that exact object, after the object's metadata row is found under row-level security.

## Group onboarding (generic)

Create Group (suggested, editable slug) → choose a Business Profile: create a new BusinessEntity **or** select an existing one for which the caller holds `business_entity.select_for_onboarding` → create the GroupBusinessEntity association → complete onboarding. Completion is impossible without the association; unauthorized or foreign identifiers cannot complete the flow (STAGE-ONBOARDING).

## Labels (generic guidance)

`LabelDefinition(organization_id, owner_scope_type, owner_scope_id, name, visibility)` and `LabelAssignment(label_id, target_type, target_id)`. Authorization validates both the label's owner scope and the target's scope. Organization-owned labels on Groups are administrative metadata, private from Group members by default (STAGE-LABELS).

## Hostname routing

Browser requests resolve an Organization selector from `{organization_slug}.${PLATFORM_BASE_DOMAIN}` or a verified custom domain that points at `CUSTOM_DOMAIN_CNAME_TARGET`, after trusted normalization (lowercase, trailing dot and port removed). Unknown, unverified and look-alike hosts resolve to nothing. A resolved Organization is only routing context: authenticated access still requires an Organization binding and capability (STAGE-DOMAINS).
