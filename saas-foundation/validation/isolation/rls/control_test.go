package rls

import (
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"example.invalid/archetype/isolation/internal/fixture"
)

// Control tests prove that the fixture data would reveal a bypass or a leak.
// They use the init superuser or a deliberately unsafe setting, are reported
// separately, and are never counted as assertions.

// Case 6 [Control]: a committed foreign UPDATE/DELETE from the runtime role
// changed nothing; the superuser sees GB1 exactly as seeded.
func TestControl_ForeignRowUnchanged(t *testing.T) {
	fixture.Label(t, fixture.ControlLabel)
	ctx := testCtx(t)
	runtime := fixture.RuntimeDB(t)
	err := fixture.WithTenantTx(ctx, runtime, adaAlpha, func(tx pgx.Tx) error {
		for _, sql := range []string{`UPDATE app.groups SET name = 'Tampered' WHERE id = $1`, `DELETE FROM app.groups WHERE id = $1`} {
			tag, err := tx.Exec(ctx, sql, fixture.GroupGB1)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 0 {
				return fmt.Errorf("%s affected %d rows; rolling back", sql, tag.RowsAffected())
			}
		}
		return nil // commit: nothing was changed
	})
	if err != nil {
		t.Fatal(err)
	}
	super := fixture.SuperuserSetup(t)
	var org, slug, name string
	if err := super.QueryRow(ctx, `SELECT organization_id::text, slug, name FROM app.groups WHERE id = $1`, fixture.GroupGB1).Scan(&org, &slug, &name); err != nil {
		t.Fatal(err)
	}
	requireEqual(t, "GB1 organization", org, fixture.OrgBravo)
	requireEqual(t, "GB1 slug", slug, "gb-one")
	requireEqual(t, "GB1 name", name, "Group B One")
}

// Case 12 [Control]: a session-level set_config (is_local = false) WOULD
// leak tenant context to the next user of a pooled connection. This is why
// fixture.SetTenant is transaction-local.
func TestControl_SessionSetConfigLeaks(t *testing.T) {
	fixture.Label(t, fixture.ControlLabel)
	ctx := testCtx(t)
	pool := fixture.RuntimeDBSingle(t)
	var pid1, pid2 int
	if err := pool.QueryRow(ctx, `SELECT pg_backend_pid(), set_config('app.organization_id', $1, false), set_config('app.user_id', $2, false)`,
		fixture.OrgAlpha, fixture.IdentityAda).Scan(&pid1, new(string), new(string)); err != nil {
		t.Fatal(err)
	}
	// Restore the connection before the pool is closed, whatever happens.
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `SELECT set_config('app.organization_id', '', false), set_config('app.user_id', '', false)`)
	})
	// The "next request" applies no context, yet sees tenant rows.
	var leaked int
	if err := pool.QueryRow(ctx, `SELECT pg_backend_pid(), (SELECT count(*) FROM app.groups)`).Scan(&pid2, &leaked); err != nil {
		t.Fatal(err)
	}
	requireEqual(t, "same backend", pid2, pid1)
	requireEqual(t, "groups visible through leaked session context", leaked, fixture.SeedGroupsAlpha)
}

// Case 22 [Control]: the superuser sees B's rows for the case-3 query, so
// the fixture data would reveal a bypass.
func TestControl_BypassSeesForeign(t *testing.T) {
	fixture.Label(t, fixture.ControlLabel)
	ctx := testCtx(t)
	super := fixture.SuperuserSetup(t)
	requireEqual(t, "GB1 visible to superuser", count(t, ctx, super, `SELECT count(*) FROM app.groups WHERE id = $1`, fixture.GroupGB1), 1)
	requireEqual(t, "all groups visible to superuser", count(t, ctx, super, `SELECT count(*) FROM app.groups`),
		fixture.SeedGroupsAlpha+fixture.SeedGroupsBravo)
}

// Case 24 [Control]: the runtime role's binding change touches exactly one
// role_bindings row and no other app table. Everything runs in one
// superuser transaction (the change itself under SET LOCAL ROLE runtime, so
// RLS applies) and is rolled back.
func TestControl_SingleRowChange(t *testing.T) {
	fixture.Label(t, fixture.ControlLabel)
	ctx := testCtx(t)
	env := fixture.LoadEnv(t)
	super := fixture.SuperuserSetup(t)
	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tables := ids(t, ctx, tx, `
		SELECT c.relname::text FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'app' AND c.relkind IN ('r', 'p')`)
	snapshot := func() map[string]string {
		out := map[string]string{}
		for _, tbl := range tables {
			var h string
			q := `SELECT md5(coalesce(string_agg(t::text, '|' ORDER BY t::text), '')) FROM ` + ident(tbl) + ` t`
			if err := tx.QueryRow(ctx, q).Scan(&h); err != nil {
				t.Fatalf("hash %s: %v", tbl, err)
			}
			out[tbl] = h
		}
		return out
	}
	bindingRows := func() []string {
		return ids(t, ctx, tx, `SELECT t::text FROM app.role_bindings t`)
	}
	asRuntime := func(sql string, args ...any) int64 {
		t.Helper()
		mustExec(t, ctx, tx, `SET LOCAL ROLE `+pgx.Identifier{env.RuntimeRole()}.Sanitize())
		if err := fixture.SetTenant(ctx, tx, adaAlpha); err != nil {
			t.Fatal(err)
		}
		n := mustExec(t, ctx, tx, sql, args...)
		mustExec(t, ctx, tx, `RESET ROLE`)
		return n
	}
	compare := func(step string, before, after map[string]string) {
		t.Helper()
		for _, tbl := range tables {
			changed := before[tbl] != after[tbl]
			if tbl == "role_bindings" && !changed {
				t.Fatalf("%s: role_bindings unchanged", step)
			}
			if tbl != "role_bindings" && changed {
				t.Fatalf("%s: app.%s changed", step, tbl)
			}
		}
	}
	diff := func(a, b []string) (onlyA, onlyB int) {
		in := func(xs []string, x string) bool {
			for _, y := range xs {
				if y == x {
					return true
				}
			}
			return false
		}
		for _, x := range a {
			if !in(b, x) {
				onlyA++
			}
		}
		for _, x := range b {
			if !in(a, x) {
				onlyB++
			}
		}
		return
	}

	h0, r0 := snapshot(), bindingRows()
	requireEqual(t, "downgrade rows", asRuntime(
		`UPDATE app.role_bindings SET role = 'group_member' WHERE subject_identity_id = $1 AND group_id = $2 AND source = 'membership'`,
		fixture.IdentityLin, fixture.GroupGA1), int64(1))
	h1, r1 := snapshot(), bindingRows()
	compare("downgrade", h0, h1)
	if a, b := diff(r0, r1); a != 1 || b != 1 {
		t.Fatalf("downgrade: %d rows removed and %d added in role_bindings, want 1 and 1", a, b)
	}

	requireEqual(t, "revoke rows", asRuntime(
		`DELETE FROM app.role_bindings WHERE subject_identity_id = $1 AND group_id = $2 AND source = 'membership'`,
		fixture.IdentityLin, fixture.GroupGA1), int64(1))
	h2, r2 := snapshot(), bindingRows()
	compare("revoke", h1, h2)
	if a, b := diff(r1, r2); a != 1 || b != 0 {
		t.Fatalf("revoke: %d rows removed and %d added in role_bindings, want 1 and 0", a, b)
	}
}
