# Foundation follow-up

The archetype renders a minimal, verified skeleton. The obligations below are **prescribed, not yet implemented**: each foundation stage must implement them and prove them with the stated verification. None of them is passed by the skeleton alone. Treat this list as the backlog of foundation work before any product feature is built.

Stages are generic; map them to your own planning identifiers.

## STAGE-BOOTSTRAP — repository and local platform
- [ ] Adopt the rendered layout; keep root names describing responsibility (`web`, `core`, `intelligence`, `contracts`, `infra`).
- [ ] Choose and pin a migration tool (for example goose, golang-migrate or Atlas); run migrations as the migrator role through `SET ROLE <db>_owner`, in a container.
- [ ] Add the MCP server initialization in `core` (official MCP Go SDK) only together with authentication from STAGE-IDENTITY; never expose an unauthenticated MCP endpoint.
- Verify: clean container-only start; migration up/down in a disposable database; rename via `.env` only.

## STAGE-WEB-SHELL — web shell and design tokens
- [ ] Initialize shadcn/ui with Base UI primitives (`shadcn init --base base --template next … --yes`), keeping components on semantic tokens.
- [ ] Accessible generic layout; keyboard operation; Organization token overrides.
- [ ] Keep `web` a thin BFF: no authority, policy or domain rules; server-only calls to `core`; no `NEXT_PUBLIC_*` backend URLs.
- Verify: keyboard navigation test; token-override visual check; lint rule or test that forbids direct browser calls to internal services.

## STAGE-OBSERVABILITY — health, logs, traces, metrics, CI
- [ ] Metrics export and trace propagation across web → core → intelligence.
- [ ] Contract validation for cross-service schemas in CI.
- [ ] Containerized build/lint/test CI using the checks profile; keep change evidence (who, what, when, review).
- Verify: dependency-outage readiness tests; trace continuity test; CI run evidence.

## STAGE-IDENTITY — global identity and sessions
- [ ] Integrate an external identity provider behind an `IdentityProvider` interface; do not build a custom authentication provider.
- [ ] Session lifecycle, logout, revocation; MFA and SSO capability.
- [ ] Any development-only identity adapter must be behind a build tag or explicit flag and must refuse to start when `APP_ENV=production`.
- Verify: one identity in several Organizations with different roles; revoked session rejected on the next request; production refuses the development adapter.

## STAGE-TENANCY — Organization, Group, BusinessEntity
- [ ] `organization_id` on every tenant-owned row; `TenantContext` is a security envelope, never a persisted tenant table.
- [ ] Group: immutable UUID, editable kebab-case slug, `UNIQUE (organization_id, slug)`, reserved-route validation; slugs never grant access.
- [ ] BusinessEntity shared by many Groups through GroupBusinessEntity, with same-Organization enforcement by composite foreign keys **and** service validation.
- [ ] Memberships keep their canonical shape (user, scope, role); confirm how membership roles derive from RoleBinding so there is a single mutable authority store.
- [ ] Preserve BusinessEntity → many BillingProfiles without implementing billing.
- Verify: cross-Organization association rejected by the service and by the database; slug collision and reserved-name tests.

## STAGE-POLICY — shared RoleBinding policy
- [ ] One policy system over `RoleBinding(subject, scope_type, scope_id, role, source)`; deny by default.
- [ ] Capability-specific inheritance from Groups to BusinessEntities computed at evaluation time; no mirrored permission rows.
- [ ] Sensitive operations (ownership, privileged settings, policy, audit, role grants) require direct BusinessEntity or Organization authority.
- [ ] Context resolved on the server from the authenticated session; client and model identifiers are selectors only.
- Verify: permission-matrix tests; forged identifiers behave like nonexistent ones; role removal changes access with one row change.

## STAGE-RLS — PostgreSQL row-level security and roles
- [ ] ENABLE and FORCE row-level security on every tenant table; per-command policies; same-tenant composite foreign keys.
- [ ] Transaction-local context (`set_config(..., true)`) in every request transaction; missing or malformed context fails closed.
- [ ] Runtime role: never superuser, never BYPASSRLS, never owner or member of the owner; SECURITY DEFINER functions narrowly scoped, `search_path` pinned, allowlisted.
- [ ] Production role provisioning: managed PostgreSQL does not run init scripts; create the same role separation through provisioning.
- Verify: runtime-role attack suite (cross-Organization reads and writes, absent and malformed context, pooled connection reuse), privileged-connection guards, owner FORCE tests.

## STAGE-STORAGE — object storage
- [ ] `ObjectStore` interface with a provider adapter; metadata rows in PostgreSQL anchor authorization.
- [ ] Signed URLs only after exact-object authorization; prefixes confer nothing; server-generated opaque keys; capped expiry; issuance audited.
- [ ] Scoped, server-resolved credentials (never root credentials in the application).
- [ ] Re-evaluate the local object store before any non-local use (see `VENDORS.md`).
- Verify: foreign-object and prefix-guess denials; expired and sibling-key URLs fail; anonymous bucket access denied.

## STAGE-AUDIT — audit and events
- [ ] Durable AuditEvent with actor, Organization, optional Group, resource, action, outcome, timestamp, trace and request identifiers.
- [ ] Record security denials, context changes, membership and role changes and administrative mutations, in the same transaction as the mutation.
- [ ] Append-only by privileges **and** database controls; deletion only through a retention role and policy.
- Verify: completeness tests per event category; modification attempts denied for every role; retention deletes only expired rows.

## STAGE-OPS — secure operations and SOC 2 readiness evidence
- [ ] Secrets-provider implementation for production (the skeleton's `SecretProvider` reads the environment).
- [ ] Access provisioning and deprovisioning; dependency and vulnerability scanning (for example govulncheck, npm audit, pip-audit, an image scanner) with a pinned or cached vulnerability database.
- [ ] Encryption in transit and at rest for production; backup and restore procedures with periodic restore tests; retention and deletion hooks; incident runbooks; vendor inventory upkeep.
- Verify: restore into an isolated environment; deprovisioning removes access; scan failures block CI. Readiness evidence is not a compliance claim.

## STAGE-MCP — control plane and dynamic tools
- [ ] Control-plane tools: `account.get_current_user`, `account.list_organizations`, `account.get_current_context`, `account.switch_organization`, `context.list_groups`, `context.switch_group`, `logout`.
- [ ] Switching validates membership or grant, clears incompatible Group scope and recalculates the tool catalog; ordinary tool arguments never switch tenants.
- [ ] `tools/list` shows only capabilities available in the active context; every execution reauthorizes identity, context, membership, resource ownership, capability and approval; stale catalogs fail at execution.
- Verify: hidden-tool calls fail like unknown tools; revoked or downgraded roles, policy changes and expired sessions fail at execution with a policy denial.

## STAGE-DOMAINS — hostnames and custom domains
- [ ] Platform subdomains from `PLATFORM_BASE_DOMAIN`, verified custom domains through `CUSTOM_DOMAIN_CNAME_TARGET`, trusted hostname normalization, TLS lifecycle adapter.
- [ ] A hostname resolves an Organization selector only; authenticated access still requires authorization.
- Verify: unknown, unverified and look-alike hosts resolve nothing; hostname alone grants nothing.

## STAGE-ONBOARDING — Group and BusinessEntity onboarding
- [ ] Create Group, then create or select an existing BusinessEntity under sufficient owner or admin authority, then create the association, then complete onboarding.
- [ ] Scoped invitations with expiry, revocation and replay protection.
- Verify: unauthorized selection fails; completion is impossible without the association; invitation replay fails.

## STAGE-LABELS — administrative labels
- [ ] Generic LabelDefinition and LabelAssignment with owner scope, target type and visibility; Organization-private metadata is private by default.
- Verify: owner and target authorization; cross-scope rejection; default privacy from Group members.

## STAGE-AI — internal intelligence (only when enabled)
- [ ] Core owns the public streaming lifecycle (SSE, WebSocket or streaming HTTP), cancellation and deadlines, tool approvals and commit gates; it translates intelligence events and never exposes them directly to the browser.
- [ ] Core-signed, short-lived, audience-bound execution envelopes (the skeleton's static service token is a starting point only).
- [ ] Authorized callbacks: a callback presents its grant reference; core reauthorizes against current grants and ignores tenant identifiers supplied by intelligence.
- [ ] Retrieval filtered before the model; scoped memory; durable usage records built by core from its own envelope (user, Organization, Group, BusinessEntity) joined to usage events; observability vendors are never the ledger and receive no content by default.
- [ ] Decide whether embeddings need a direct-provider adapter behind `ModelGateway`.
- Verify: revoked grant denies callbacks; forged attribution in events rejected; retrieval leakage tests; trace privacy tests.
