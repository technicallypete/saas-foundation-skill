# Service boundaries and naming

## Responsibilities

| Directory | Responsibility | Must never |
|---|---|---|
| `web/` | Next.js UI and thin server-side bridge (BFF/session bridge). Tailwind with semantic white-label tokens; shadcn/ui with Base UI primitives at the web-shell stage | Hold authority, policy or domain rules; call `intelligence` or data stores; expose backend URLs through `NEXT_PUBLIC_*` |
| `core/` | Go modular monolith: application and domain services, identity and tenant control plane, authorization, official MCP Go SDK transport, durable state, audit, public streaming lifecycle. MCP handlers delegate to services | Put business rules in transport handlers; trust client or model identifiers as authority |
| `intelligence/` (optional) | Internal Python/FastAPI inference execution from core-authorized envelopes; typed events back to core | Establish tenant authority, hold data-store credentials, mutate authoritative state, be reachable from the browser or `web` |
| `contracts/` | Cross-service schemas and protocols (`contracts/intelligence/*.schema.json`) | Contain business logic or code |
| `infra/` | Compose (`infra/compose/local.yml`), images (`infra/docker/`), database and storage setup (`infra/postgres/`, `infra/minio/`), security tooling configuration (`infra/security/`), later deployment | Contain application code |
| `docs/`, `management/`, `skills/` | Documentation, goal lifecycle, reusable agent guidance | Be written by the renderer (`docs/foundation/` excepted) |

Dependency direction: **web → core → {PostgreSQL, Redis, MinIO, intelligence}**. Compose enforces it with networks: `edge` (web, core), `data` (core and data stores), `inference` (core, intelligence). The validation harness proves it (`S-ENV-SCOPE`, `S-DEPS`, `T-BOUNDARIES`).

## Naming rules

- Root directories name responsibilities, never tiers or technologies: no `frontend/`, `backend/`, `api/`, `app/` or `services/` at the root.
- A child never repeats its parent's meaning: no `core/internal/core`, `core/cmd/core`, `core/cmd/api`, `api/api`, `app/app`, `intelligence/app`, `intelligence/src/intelligence`.
- Preferred paths: `web/app` (Next.js convention), `core/cmd/server`, `core/internal/<responsibility>`, `intelligence/src`, `infra/compose/local.yml`.
- No placeholder modules or empty directories. Generated `core/internal/` holds only `config/`, `health/` and `observability/` until a foundation stage adds a real responsibility.

## Packaging exceptions (explained, not accidental)

- **Python:** `intelligence/src/` contains top-level modules (`models`, `providers`, `security`, `observability`, `streaming`) run with `PYTHONPATH` pointing at `src`. There is no `src/intelligence/` package and the project is not installed, so generic module names cannot shadow installed packages. The ASGI entry point is `src/main.py` (`uvicorn main:app`).
- **Dockerfiles** live in `infra/docker/` with each service directory as the build context; `<name>.Dockerfile.dockerignore` sits beside each Dockerfile. The intelligence image receives `contracts/` through a named build context (`contracts`).
- **Go module names** are neutral (`core`) so generated source is identical for every product; rename the module when publishing it.
- **The MCP endpoint is not templated.** It is added together with authentication at the identity stage so that no unauthenticated MCP endpoint ever exists.

## Intelligence disabled

When `intelligence_enabled` is false, `intelligence/`, its Dockerfile, its Compose service, the `inference` network, `contracts/intelligence/` and every `INTELLIGENCE_*`, `AI_MODEL_*` and `OPENROUTER_API_KEY` setting are omitted (not kept as guidance). Re-rendering later with intelligence enabled creates those files and proposes `.env.example` and `infra/compose/local.yml` changes for manual reconciliation.
