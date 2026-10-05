# Docker-first runtime

## Host prerequisites

- Git; Docker Engine or Docker Desktop with Compose 2.24 or later (WSL integration on Windows); a POSIX shell with coreutils.
- **Not required and never invoked by project workflows:** Go, Python, Node, npm, PostgreSQL, Redis, MinIO, `psql`, `redis-cli`, `mc`, cloud CLIs.
- **Named exception:** authoring agents (for example coding assistants) and optional IDE or editor plugins may bring their own runtimes. They are authoring aids only; every build, check, migration and test must keep working with them absent. The validation harness proves this by shadowing those runtimes (`T-DOCKER-ONLY`), including Windows executables exposed to WSL through `/mnt/*`.

## Compose topology (`infra/compose/local.yml`)

| Service | Image / build | Internal port | Host port (default) | Networks |
|---|---|---|---|---|
| `postgres` | `pgvector/pgvector:0.8.7-pg17` | 5432 | `POSTGRES_PORT` (5532) | data |
| `redis` | `redis:8.10.2-alpine`, no persistence, password required | 6379 | `REDIS_PORT` (6479) | data |
| `minio` | built from final upstream source (`infra/docker/minio.Dockerfile`) | 9000 / 9001 | `MINIO_API_PORT` (9100) / `MINIO_CONSOLE_PORT` (9101) | data |
| `minio-init` | same Dockerfile, `init` target; one-shot | — | — | data |
| `core` | `infra/docker/core.Dockerfile` (distroless runtime) | 8180 | `CORE_PORT` (8180) | edge, data, inference* |
| `web` | `infra/docker/web.Dockerfile` (Next.js standalone) | 3000 | `WEB_PORT` (3100) | edge |
| `intelligence`* | `infra/docker/intelligence.Dockerfile` | 8000 | `INTELLIGENCE_PORT` (8100) | inference |
| `core-check`, `web-check`, `intelligence-check`*, `secret-scan` | `check` targets / gitleaks; profile `checks`, `network_mode: none` | — | — | none |

\* when intelligence is enabled.

- Host ports default to the conventional port + 100 where a standard exists (Go core has none; 8180 is the chosen default) and bind to `BIND_ADDRESS` (`127.0.0.1`). Containers always use conventional internal ports and service DNS, so changing host ports never breaks internal connections.
- PostgreSQL and MinIO use named volumes (durable). Redis is ephemeral by design: `--save "" --appendonly no`.
- Every long-running service has a healthcheck; core waits for healthy data stores and a completed `minio-init`; web waits for a healthy core.

## Health and readiness

| Endpoint | Meaning |
|---|---|
| core `GET /healthz` | Liveness; no dependencies |
| core `GET /readyz` | PostgreSQL (as the runtime role), Redis (AUTH + PING) and the object store (scoped `HeadBucket`) gate readiness; intelligence is reported under `optional` and never gates it. 503 names the failing dependency, never error detail |
| web `GET /api/health` / `GET /api/ready` | Liveness / requires core ready |
| intelligence `GET /healthz`, `GET /readyz` | Liveness / configuration loaded; content-free |

## Start, check, stop

The destination's `docs/foundation/GETTING-STARTED.md` contains the canonical commands in marked executable blocks (`<!-- archetype:start|check|stop -->`); the validation harness executes them verbatim.

- Start: `docker compose --env-file .env -f infra/compose/local.yml up -d --build --wait`
- Check: `docker compose --env-file .env -f infra/compose/local.yml --profile checks run --rm --build <service>-check`, then `secret-scan`
- Stop: `docker compose --env-file .env -f infra/compose/local.yml down` (add `-v` to destroy local data)

## Hot reload (prescribed, STAGE-BOOTSTRAP)

Use Compose `develop.watch` or bind mounts with container-owned dependency directories (`node_modules`, Go caches) and file-change polling where needed (`WATCHPACK_POLLING=true` on Docker Desktop). Never make host runtimes part of the loop. Run development containers as the host user, or keep their writes in named volumes, so no root-owned files appear in the working tree.

## Images

Pin exact tags (never `latest`) and record digests in change evidence. Runtime images run as non-root users. Validation-only tools (shellcheck, hadolint, yq, busybox) are never part of the generated project.
