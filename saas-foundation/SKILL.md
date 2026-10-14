---
name: saas-foundation
description: Generate or re-apply a generic multi-tenant SaaS foundation (Go core, Next.js web, optional internal Python/FastAPI intelligence, PostgreSQL with row-level security, Redis, S3-compatible storage, Docker Compose) with tenant isolation, shared RoleBinding policy, audit and MCP context-switching foundations. Use when bootstrapping a new multi-tenant SaaS repository or re-applying this foundation layout to an existing one; not for adding product features or provisioning cloud resources.
license: MIT
metadata:
  version: "0.1.0"
---

# SaaS Foundation: multi-tenant SaaS archetype

This Skill renders a minimal, verified foundation skeleton into a destination repository and hands off the foundation work that remains. It is project-agnostic: product name, domains, database name, providers, ports and the optional intelligence service are inputs, never assumptions.

## When to use

- Bootstrapping a new multi-tenant SaaS repository on this architecture.
- Re-applying the foundation layout to an existing repository (the renderer never overwrites; differences become proposals).

Do **not** use it to add product features, product-specific schemas or prompts, or to provision cloud accounts, domains or model providers.

## Prerequisites

- Git, Docker Engine or Docker Desktop with Compose 2.24 or later (WSL integration on Windows), and **bash** with coreutils (the renderer and harness are bash scripts).
- **No host Go, Python, Node, PostgreSQL, Redis or object-store server.** Every build, check, migration and test runs in containers. Authoring agents and optional IDE plugins may carry their own runtimes; that is a named exception and never part of the project's normal path ([docker-runtime.md](references/docker-runtime.md)).
- Network access for image and package downloads on first use.

## Essential invariants

These hold for every output and every later stage. Details and requirement IDs: [requirements-catalog.md](references/requirements-catalog.md).

1. **Service boundaries.** `web` is the UI and a thin server-side bridge; `core` owns facts, authority, policy, MCP, audit and the public edge; optional `intelligence` owns internal inference only; `contracts` holds cross-service schemas only; `infra` holds Compose, images and database/storage setup. Dependency direction: web → core → {data stores, intelligence}. ([service-boundaries.md](references/service-boundaries.md))
2. **Non-redundant naming.** Root directories name responsibilities; never `frontend/`, `backend/`, `api/`, `app/` or `services/`, and never paths such as `core/internal/core`.
3. **Configuration-only identity.** Product name, platform domain, custom-domain CNAME target and database name exist only in `.env.example`/`.env` and are read at runtime (`PRODUCT_NAME`, `PLATFORM_BASE_DOMAIN`, `CUSTOM_DOMAIN_CNAME_TARGET`, `DATABASE_NAME`). No literal in source, prompts, metadata or generated text. ([configuration.md](references/configuration.md))
4. **Docker-first.** Host ports default to the conventional port + 100 (web 3100, core 8180, intelligence 8100, PostgreSQL 5532, Redis 6479, MinIO 9100/9101), bind to loopback, and are configurable; containers use conventional internal ports and service DNS.
5. **Tenancy.** Identity is global; authority is scoped. Organization is the tenant boundary and `organization_id` is on every tenant-owned row; `TenantContext` is a security envelope, never a persisted tenant table. Groups have immutable UUIDs and editable, Organization-unique kebab-case slugs that never grant access. A BusinessEntity can belong to many Groups, with same-Organization integrity in the database **and** the service. ([tenancy-authorization.md](references/tenancy-authorization.md))
6. **One policy system.** `RoleBinding(subject, scope_type, scope_id, role, source)` is the single mutable authority store; membership entities are projections of it. Deny by default; capability-specific Group → BusinessEntity inheritance computed at evaluation time; no mirrored rows; sensitive operations need direct BusinessEntity or Organization authority.
7. **Server-resolved context.** Client- and model-supplied identifiers are selectors, never authority. Hostnames select an Organization; authorization still applies.
8. **Database defense in depth.** ENABLE + FORCE row-level security; separate owner, migrator and runtime roles; the runtime role is never superuser, BYPASSRLS or owner; tenant context is transaction-local and missing or malformed context fails closed. ([database-rls.md](references/database-rls.md))
9. **Storage.** Signed URLs only after exact-object authorization; prefixes confer nothing; scoped, server-resolved credentials. ([storage-audit.md](references/storage-audit.md))
10. **Audit.** Durable, attributed AuditEvents for denials, context changes, membership/role changes and administrative mutations; modification controlled by privileges and database controls, not by an append-only API alone.
11. **MCP control plane.** Explicit Organization and optional Group switching; `tools/list` shows only what the active context allows; every execution reauthorizes; stale catalogs fail. ([mcp-control-plane.md](references/mcp-control-plane.md))
12. **Optional intelligence.** Internal only, envelope-driven, no data-store credentials, typed events; models configured as `AI_MODEL_<CAPABILITY>=<provider>:<model>`; OpenRouter is the default adapter behind `ModelGateway`; no `AI_GATEWAY_DEFAULT`. Disabled means no Python service and no AI credentials. ([intelligence-ai.md](references/intelligence-ai.md))
13. **Secrets.** Never in templates, logs, manifests or traces; `.env` is local and never committed.
14. **Readiness, not compliance.** These are SOC 2 readiness foundations, not a compliance claim. ([security-operations.md](references/security-operations.md))
15. **Destination safety.** The renderer never overwrites, deletes, writes through symlinks, writes secrets, or touches `AGENTS.md`, `CLAUDE.md`, `README.md`, `.env`, `docs/ARCHITECTURE.md`, `management/` or `skills/`.
16. **Even when another provider supplies permissions,** audit, storage authorization, row-level security and context-switch checks remain mandatory.

## Inputs

Write a JSON input file. Full contract, validation rules and defaults: [configuration.md](references/configuration.md). Neutral sample: [examples/neutral.input.json](examples/neutral.input.json).

| Field | Required | Notes |
|---|---|---|
| `product_name` | yes | Display name; never invented by the agent |
| `platform_base_domain` | yes | Lowercase hostname |
| `database_name` | yes | `^[a-z_][a-z0-9_]{0,47}$` |
| `intelligence_enabled` | yes | Explicit boolean |
| `project_slug` | no | Derived from the product name |
| `custom_domain_cname_target` | no | Defaults to `custom.<platform_base_domain>` |
| `identity_provider` | no | Label only (default `oidc`); no integration is rendered |
| `production_object_store` | no | `r2`, `s3` or `s3-compatible` |
| `ports` | no | Host-port overrides; must be unique, 1024-65535 |
| `ai_models` | no | Only when intelligence is enabled; `provider:model` identities |

## Workflow

Paths are relative to this Skill's directory. Follow [checklists/invocation.md](checklists/invocation.md).

1. **Preflight.** Confirm Docker and Compose versions; that the destination exists and is not this Skill; and that the host ports to be used (defaults 3100, 8180, 8100, 5532, 6479, 9100, 9101) are free — otherwise collect `ports` overrides. Recommend a clean git working tree or a new branch; if the destination is not a git repository, ask whether to `git init` it (the renderer works either way). First use downloads images and compiles MinIO from source (several minutes); later runs are cached.
2. **Collect inputs.** Ask the user for every required value. Never invent product identity. Write the input file outside the destination or in a scratch location.
3. **Validate.** `generator/render.sh validate --input <file>` prints `resolved\t<json>` (every value with defaults applied: slug, CNAME target, ports, providers) and `output_id`. Show the resolved values to the user so defaults are seen before anything is written.
4. **Plan.** `generator/render.sh plan --input <file> --dest <dir>` (dry run; writes nothing). Show the user the planned `create` and `conflict` lines.
5. **Confirm** with the user.
6. **Render.** `generator/render.sh render --input <file> --dest <dir> [--report <file outside dest>]`.
7. **Reconcile conflicts.** For each `conflict`, show the diff between the existing file and `.archetype/outputs/<output_id>/proposed/<path>`; the user decides. Never overwrite, and never edit `AGENTS.md` or `CLAUDE.md`.
8. **Create `.env`** from `.env.example`, filling every empty secret with a locally generated **hex** value (URL-safe; for example `head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n'`, at least 16 characters). Never print or commit them. Role passwords and `DATABASE_NAME` take effect only when the PostgreSQL volume is first initialized; after changing them, recreate it (`down -v`).
9. **Start and check** with the executable blocks in the destination's `docs/foundation/GETTING-STARTED.md`. Healthy means `up --wait` exits 0 (the one-shot `minio-init` correctly ends as "Exited (0)") and core `GET /readyz` reports every dependency `ok`.
10. **Hand off** the foundation stages in `docs/foundation/FOLLOW-UP.md`, and the commit guidance in [checklists/invocation.md](checklists/invocation.md).

## Output contract

- **Files:** `.env.example`, `.gitignore` (merged), `core/`, `web/`, `contracts/`, `infra/{compose,docker,postgres,minio,security}`, `docs/foundation/{GETTING-STARTED,FOLLOW-UP,VENDORS}.md`, and, when intelligence is enabled, `intelligence/`, its Dockerfile and `contracts/intelligence/` schemas.
- **Manifest:** `.archetype/outputs/<output_id>/manifest.json`, content-addressed. `output_id` depends only on inputs, the Skill version and intended content, never on the destination state. Manifests contain no actions, timestamps, secrets or absolute paths.
- **Per-path actions** (stdout, and `--report` outside the destination): `create`, `unchanged`, `conflict`, `ensure-lines` (`.gitignore` only).
- **Exit codes:** `0` success; `1` internal error; `2` invalid input or unsafe destination (nothing written); `3` rendered with conflicts to reconcile.
- **Repeat runs:** with the same inputs and an unchanged destination, every path is `unchanged` and nothing is written.

## Management skill composition

When used with `sdlc-management` (or its Restruct SDLC successor), that skill owns delivery lifecycle,
budgets, provenance, validation sequencing and cleanup. This skill retains its architectural, security
and renderer-safety constraints; management policy cannot waive them. No management skill is required
to run this package. See [verification guidance](checklists/verification.md#proportionate-validation-and-closeout).

## Verification

- In a generated project: the `check` block of `docs/foundation/GETTING-STARTED.md` (formatting, vet, unit tests, lint, type checks, secret scan; containerized, no network).
- For the Skill itself: `validation/run.sh all [--denylist FILE] [--repo-allow REGEX]` runs the static, generator, intelligence, isolation and trial suites in containers. See [checklists/verification.md](checklists/verification.md).
- A pass proves the skeleton, the destination safety and the minimal isolation fixtures. It does **not** prove the later foundation stages, production security or compliance; those are listed in `docs/foundation/FOLLOW-UP.md`.

## References

- [service-boundaries.md](references/service-boundaries.md): boundaries, naming, dependency direction, packaging exceptions.
- [configuration.md](references/configuration.md): input and environment contracts, validation, collision policy, exit codes.
- [docker-runtime.md](references/docker-runtime.md): host prerequisites, Compose topology and networks, ports, health, start/check/stop.
- [tenancy-authorization.md](references/tenancy-authorization.md): identity, tenancy, RoleBinding, permission matrix, onboarding, labels, hostnames.
- [database-rls.md](references/database-rls.md): roles, row-level security, tenant context, pooling, privileged paths.
- [storage-audit.md](references/storage-audit.md): ObjectStore boundary, signed URLs, AuditEvent controls.
- [mcp-control-plane.md](references/mcp-control-plane.md): context switching, dynamic tools, per-execution reauthorization.
- [intelligence-ai.md](references/intelligence-ai.md): model configuration, ModelGateway, envelopes, typed events, usage and privacy.
- [security-operations.md](references/security-operations.md): SOC 2 readiness hooks and operational controls.
- [commodity-composition.md](references/commodity-composition.md): composed tools and skills, assumptions and fallbacks.
- [requirements-catalog.md](references/requirements-catalog.md): every requirement with its enforcement class and verification.
- [checklists/foundation-follow-up.md](checklists/foundation-follow-up.md): prescribed foundation stages (rendered as `docs/foundation/FOLLOW-UP.md`).
