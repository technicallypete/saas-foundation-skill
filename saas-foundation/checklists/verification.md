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

## Proportionate validation and closeout

Choose checks for changed behavior and its dependencies. The full package harness
qualifies the renderer, fixtures and portability; it is not a default test suite
for every feature in a generated application. Keep the suite requirements above
for package qualification and retain relevant isolation/security coverage. Changes
to CI or a test harness need their covering controls; expensive CI meta-tests need
not accompany unrelated application changes. Unknown impact takes the broader path.
The adopting repository's actual CI selector and approved Goal requirements govern
what runs; this guidance installs no selector or new CI implementation.

Use Git commit/path references for committed source and compact Results recording
commands, outcomes (including failures), relevant runtime inputs and required proof.
Use additional snapshots only for relevant inputs Git does not identify; preserve
existing approved manifests/bindings. When a management skill governs the repository,
follow its target-delta/reconciliation policy; do not restart unchanged tests solely
because a status document or commit SHA changed.

At verified terminal closeout, retain the compact Result and necessary proof; purge
disposable task-owned bulk logs, reports and build caches under the repository's
cleanup policy. Record ownership when creating resources, remove only attributable
unused resources, and retain active/shared data, secrets and unproven items. Preserve
a compact cleanup receipt and item-specific holds instead of creating a second
resource registry or a new cleanup campaign.
