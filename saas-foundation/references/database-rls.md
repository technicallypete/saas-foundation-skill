# PostgreSQL roles and row-level security

## Role separation (rendered: `infra/postgres/init/10-roles.sh`)

| Role | Login | Purpose | Never |
|---|---|---|---|
| `<db>_owner` | no | Owns schema `app` and every object in it | Used by the application |
| `<db>_migrator` | yes | Runs migrations via `SET ROLE <db>_owner` (`GRANT … WITH INHERIT FALSE, SET TRUE`) | Inherits owner privileges implicitly |
| `<db>_runtime` | yes | The only role the application connects as | Superuser, BYPASSRLS, CREATEROLE, CREATEDB, owner, or member of owner |
| `<db>_retention` | yes | Audit-retention deletions only | Reads audit content or modifies recent rows |

Role names derive from `DATABASE_NAME` at initialization. The image's init superuser is used only by initialization. `PUBLIC` loses all database privileges and `CREATE` on `public`; new tables are unreachable until explicitly granted; functions are not executable by `PUBLIC` by default. Managed PostgreSQL does not run init scripts: create the same separation through provisioning (STAGE-RLS, STAGE-OPS).

## Row-level security pattern

- ENABLE **and FORCE** row-level security on every tenant table (FORCE subjects the owner path too) and on the global identity table.
- Separate per-command policies (`SELECT`, `INSERT … WITH CHECK`, `UPDATE … USING/WITH CHECK`, `DELETE`) per role; never a lone `FOR ALL … USING`.
- Context helpers read transaction-local settings and fail closed:

```sql
CREATE FUNCTION app.ctx_org() RETURNS uuid LANGUAGE sql STABLE
  AS $$ SELECT NULLIF(current_setting('app.organization_id', true), '')::uuid $$;
```

  Empty or missing context → `NULL` → no row matches; a malformed value raises an error.
- Same-tenant integrity with composite foreign keys, e.g. `FOREIGN KEY (organization_id, group_id) REFERENCES groups (organization_id, id)` backed by `UNIQUE (organization_id, id)`. Foreign keys bypass RLS, so a plain `group_id` reference would allow cross-tenant links.
- Make `id` and `organization_id` immutable (trigger or column grants).
- Global uniqueness (identity e-mail, Organization slug) leaks existence; answer uniformly at the API.

## Tenant context per transaction

```go
tx, _ := pool.Begin(ctx)
defer tx.Rollback(ctx)
tx.Exec(ctx, `SELECT set_config('app.organization_id', $1, true),
                     set_config('app.user_id', $2, true),
                     set_config('app.group_id', $3, true)`, org, user, group)
// ... queries ...
tx.Commit(ctx)
```

`set_config(..., true)` is transaction-local and resets at COMMIT or ROLLBACK, so pooled connections never carry context to the next request. Never use session-level `SET` or `set_config(..., false)`. Calling `set_config(..., true)` outside a transaction affects only that statement. The fixture's `WithTenantTx` is the reference implementation.

## SECURITY DEFINER, views and other pitfalls

- Group-scope visibility needs membership lookups that would make policies recurse. Use **narrowly scoped** `SECURITY DEFINER STABLE` helpers that read only the authority tables for the current context, pin `search_path`, are owned by a dedicated NOLOGIN, NOBYPASSRLS role that reads through role-specific SELECT-only policies, have `EXECUTE` revoked from `PUBLIC` and granted to the runtime role only, and are checked against an allowlist.
- Views must use `security_invoker = true`; materialized views have no RLS.
- `SET row_security = off` does not bypass RLS; it makes filtered queries fail.
- `COPY FROM` is not supported on RLS tables; `INSERT … RETURNING` needs a matching `SELECT` policy.
- Never mark custom functions `LEAKPROOF`.

## Privileged administrative paths

Organization provisioning (migrator/owner tooling or an audited administrative service), audit retention (retention role) and the policy-reader helpers. None is callable by the runtime role; each is documented and audited.

## Evidence in this Skill

The isolation fixtures run every assertion through guarded connection helpers (`RuntimeDB`, `OwnerTx`, `RetentionDB`; `SuperuserSetup` only for seed data and controls) and pair every denial with a positive control and a SQLSTATE mechanism check. See [requirements-catalog.md](requirements-catalog.md) (`RLS-*`).
