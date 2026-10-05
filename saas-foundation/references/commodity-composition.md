# Commodity composition

The archetype is authoritative for architectural invariants and service boundaries. Commodity setup should be composed from mature tools rather than re-invented, provided the composition preserves those invariants.

## What is composed (always)

| Component | How it is used | Assumption / version | Fallback |
|---|---|---|---|
| Official container images (golang, node, python, pgvector, redis, alpine, distroless) | Runtimes and services | Exact pinned tags; see rendered Dockerfiles and Compose | None needed; pins are updated deliberately |
| Official MCP Go SDK | Control-plane pattern (fixtures; consumer at STAGE-MCP) | v1.8.x | — |
| `aws-sdk-go-v2` | S3-compatible ObjectStore adapter and readiness | v1.47 / s3 v1.114 | Any S3-compatible client behind `ObjectStore` |
| pgx | PostgreSQL driver and pool | v5.11 | — |
| FastAPI, Uvicorn, uv | Intelligence service runtime and locking | FastAPI 0.142, Uvicorn 0.54, uv 0.12 | — |
| Next.js, React, Tailwind CSS | Web skeleton | Next 16.3, React 19.3, Tailwind 4.3 | — |
| shadcn/ui with Base UI | Web-shell components (STAGE-WEB-SHELL): `npx shadcn init --base base --template next …` run **inside a Node container** | CLI 4.21.x | Hand-written primitives on the same semantic tokens |
| `create-next-app` (only if a consumer scaffolds instead of using the template) | Must run in a container with `--no-agents-md` so it never writes repository instruction files | Next 16.x | Use the rendered `web/` skeleton |
| gitleaks | Secret scanning (`checks` profile) | v8.30.1 | Any scanner with an equivalent no-network mode |

## Agent skills evaluated (optional, not dependencies)

Evaluated on 2026-10-04. All were unversioned, user-local skills; none is required, and none is invoked by this Skill.

| Skill | Reuse decision | Reason |
|---|---|---|
| `docker-setup` | Patterns only | Its healthcheck, one-shot init job and idempotent role SQL patterns are compatible and are carried in this Skill's templates. Its root-level compose file, default ports, superuser migrations and lack of Redis and row-level security are not |
| `nextjs-setup` | Patterns only | Docker hot-reload notes are compatible ([docker-runtime.md](docker-runtime.md)). Its Bun runtime, `app/` root, browser-side backend URLs and host scaffolding conflict with the boundaries |
| `django-project`, `bootstrap-fullstack` | Not reused | A Django core and `api/`/`app/` roots conflict with the Go core and naming rules; only the "adapters stay thin over domain services" principle carries over |
| `auth-resend` | Not reused | It builds custom magic-link authentication, which conflicts with "integrate an identity provider; do not build a custom authentication provider". Its token-hygiene points (hashed single-use tokens, short expiry, uniform responses) are noted for STAGE-IDENTITY |

If such skills are present, they may be consulted for compatible patterns only. Their names, ports, layouts and authentication approaches must not override this Skill.

## Optional authoring aids

Documentation lookup, language servers, security-guidance hooks and browser automation plugins can help authors. They may require host runtimes, which falls under the named authoring/IDE exception in [docker-runtime.md](docker-runtime.md). Skip them if they are not installed; nothing in the project workflow depends on them.
