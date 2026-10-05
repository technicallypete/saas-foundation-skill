# Verification

## In a generated project

Run the `check` block of `docs/foundation/GETTING-STARTED.md`. It runs, in containers without network access: Go formatting, vet and unit tests (core); lint and type checks (web); ruff and pytest (intelligence, when enabled); gitleaks secret scanning. Then confirm:

- `GET /readyz` on core reports PostgreSQL, Redis and the object store as `ok`;
- the web page title shows `PRODUCT_NAME`, and changing it in `.env` followed by `up -d --no-build` renames the product without a rebuild;
- host ports answer only on `BIND_ADDRESS` (loopback).

## For the Skill itself

```sh
validation/run.sh all --denylist <file> --repo-allow '<regex>' --report-dir <dir>
```

| Suite | Proves |
|---|---|
| `static` | Renderer tests, Skill frontmatter, links, paths and requirements-catalog integrity; shellcheck, hadolint, Compose validity; fail-closed environment; per-service secret and network scope; naming; dependency direction; secret scanning; denylist leakage |
| `generator` | Invalid input and unsafe destinations refused with no writes; sentinels preserved; conflicts proposed; repeat runs write nothing; stable output identity; reports; symlink refusals; enabling intelligence later |
| `intelligence` | Model configuration, OpenRouter adapter contract, internal authentication, execute endpoint and typed events, observability privacy, schemas, lint (no network) |
| `isolation` | Runtime-role RLS attack cases with paired positive controls and SQLSTATE mechanism checks; owner FORCE and retention paths through guarded privileged connections; shared policy evaluator; hostname selectors; MCP catalog, switching and per-execution reauthorization (in-memory and HTTP); storage authorization against real MinIO; backup/restore smoke |
| `trials` | Two products end to end: portability and literal confinement, executed start/check/stop docs, host reachability and loopback-only binding, network boundaries, outages, persistence, rename without rebuild, intelligence toggle, Docker-only execution, secret canary, repository cleanliness |

`--denylist` supplies brand and product-domain terms that must never appear (kept outside the Skill). `--repo-allow` lists repository paths the change may touch.

## What a pass does not prove

The later foundation stages in `docs/foundation/FOLLOW-UP.md` (identity provider integration, the production policy engine, migrations, audit wiring, MCP mounting, domains, onboarding, labels, operations controls), production security, or SOC 2 compliance.
