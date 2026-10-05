# Configuration, rendering and destination safety

## Input contract

The renderer reads one JSON object. All problems are reported together and nothing is written when any exist (exit `2`).

| Field | Required | Default | Validation |
|---|---|---|---|
| `product_name` | yes | — | 1-64 printable characters; no leading or trailing space; no `"`, `\`, `$` or backtick |
| `project_slug` | no | kebab-case of `product_name` | `^[a-z][a-z0-9-]{1,38}[a-z0-9]$`; becomes `COMPOSE_PROJECT_NAME` and the bucket name prefix |
| `platform_base_domain` | yes | — | Lowercase hostname, at least two labels (a subdomain such as `app.example.net` is fine), no scheme, port, path or wildcard |
| `custom_domain_cname_target` | no | `custom.<platform_base_domain>` | Hostname rules; must differ from the base domain |
| `database_name` | yes | — | `^[a-z_][a-z0-9_]{0,47}$` (room for role suffixes); not `postgres`, `template0`, `template1` |
| `intelligence_enabled` | yes | — | JSON boolean |
| `identity_provider` | no | `oidc` | `^[a-z][a-z0-9-]{1,31}$`; a label for configuration and the vendor inventory only |
| `production_object_store` | no | `s3-compatible` | `r2`, `s3` or `s3-compatible`; guidance and inventory only |
| `ports.{web,core,intelligence,postgres,redis,minio_api,minio_console}` | no | 3100, 8180, 8100, 5532, 6479, 9100, 9101 | Integers 1024-65535, unique among enabled services; `ports.intelligence` only with intelligence enabled |
| `ai_models.<capability>` and `ai_models.<capability>_fallbacks` | no | commented samples | Only with intelligence enabled. Capabilities `chat`, `fast`, `reasoning`, `extraction`, `embedding`. `provider:model`, split on the first colon; the provider is the model provider, never a gateway (`openrouter:` is rejected); fallbacks comma separated, no duplicates, no fallbacks without a primary |

Unknown fields, secret-looking fields (`*password*`, `*secret*`, `*token*`, `*key*`) and malformed JSON are rejected. Secrets are never inputs.

## Environment contract (`.env.example`)

`.env.example` is the **only** generated file that contains product, domain or database identity. Compose reads every value from `.env` with `${VAR:?message}`, so a missing required value fails before anything starts; optional values use `${VAR-}`.

| Group | Variables |
|---|---|
| Identity and domains | `PRODUCT_NAME`, `PLATFORM_BASE_DOMAIN`, `CUSTOM_DOMAIN_CNAME_TARGET`, `DATABASE_NAME`, `COMPOSE_PROJECT_NAME`, `APP_ENV`, `BIND_ADDRESS` |
| Host ports | `WEB_PORT`, `CORE_PORT`, `INTELLIGENCE_PORT`*, `POSTGRES_PORT`, `REDIS_PORT`, `MINIO_API_PORT`, `MINIO_CONSOLE_PORT` |
| Internal wiring | `CORE_INTERNAL_URL`, `INTELLIGENCE_URL`*, `REDIS_HOST`, `REDIS_PORT_INTERNAL`, `OBJECT_STORE_ENDPOINT`, `OBJECT_STORE_REGION`, `OBJECT_STORE_BUCKET`, `OBJECT_STORE_FORCE_PATH_STYLE`, `OBJECT_STORE_ACCESS_KEY`, `MINIO_ROOT_USER` |
| Secrets (empty, required) | `POSTGRES_SUPERUSER_PASSWORD` (initialization only), `DB_MIGRATOR_PASSWORD`, `DB_RUNTIME_PASSWORD`, `DB_RETENTION_PASSWORD`, `REDIS_PASSWORD`, `MINIO_ROOT_PASSWORD`, `OBJECT_STORE_SECRET_KEY`, `INTELLIGENCE_SERVICE_TOKEN`* |
| AI* | `AI_MODEL_<CAPABILITY>`, `AI_MODEL_<CAPABILITY>_FALLBACKS` (commented samples unless supplied), `OPENROUTER_API_KEY` (empty; optional at startup). Never `AI_GATEWAY_DEFAULT` |
| Identity provider | `IDENTITY_PROVIDER`, commented generic OIDC placeholders |

\* only when intelligence is enabled.

Each service receives only what it needs: core never receives the superuser, migrator, retention or MinIO root secrets; web receives no secret; intelligence receives no data-store setting or credential.

## Templating

Only `*.tmpl` files are template-processed: `.env.example`, `infra/compose/local.yml` (intelligence conditional), `contracts/README.md` and `docs/foundation/*` (intelligence and provider conditional, identity-free). All source code is copied verbatim, so generated source is byte-identical for every product. A path segment `dot_x` becomes `.x`. `docs/foundation/FOLLOW-UP.md` is copied from [../checklists/foundation-follow-up.md](../checklists/foundation-follow-up.md).

## Output identity and manifests

- `output_id` = first 16 hex characters of `sha256(canonical non-secret inputs ‖ Skill version ‖ sorted (path, intended-content sha256, write mode))`. It never depends on the destination's state.
- `.archetype/outputs/<output_id>/manifest.json` lists the inputs, Skill version, intended files with hashes and modes, and the follow-up path. No actions, timestamps, secrets or absolute paths.
- `.archetype/outputs/<output_id>/proposed/<path>` holds the intended content of each conflicting file.
- Invocation actions are printed as `<action>\t<path>` and, with `--report <file>`, written as JSON **outside** the destination. They are never written into the destination.

## Collision policy

| Action | When | Effect |
|---|---|---|
| `create` | Target absent | Written with `O_EXCL` and no symlink following |
| `unchanged` | Target byte-identical | No write |
| `conflict` | Target differs | Not written; proposal written under the output directory; exit `3` |
| `ensure-lines` | `.gitignore` only | Missing required lines (`.env`, `.env.local`, `.archetype/outputs/*/proposed/`) appended in a marked block; existing bytes kept as a prefix |

Repeat runs with the same inputs and an unchanged destination report every path `unchanged` and write nothing. Changed inputs produce a new `output_id` and a new output directory; earlier ones are never modified.

## Refusals (exit 2, nothing written)

A missing destination, a destination that is not a directory, is a symlink, is the filesystem root, or is this Skill or inside it; any symlinked target or parent; any path escaping the destination; a `--report` path inside the destination. The renderer never emits `AGENTS.md`, `CLAUDE.md`, `README.md`, `.env`, `docs/ARCHITECTURE.md`, `management/`, `skills/` or `.git/`, never deletes and never writes secrets.

## Exit codes

`0` success · `1` internal error · `2` invalid input or unsafe destination · `3` rendered with unresolved conflicts.



## Command output

- `validate`: `resolved<TAB><json>` (the complete non-secret configuration with defaults applied, so derived values such as the slug, CNAME target and ports can be confirmed first) and `output_id<TAB><id>`.
- `plan` and `render`: one `<action><TAB><path>` line per path, then `output_id<TAB><id>` and `result<TAB>…`. `--report <file>` (outside the destination) writes the same actions as JSON.
