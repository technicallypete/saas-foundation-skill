package rls

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"example.invalid/archetype/isolation/internal/fixture"
)

// Case 1 [Runtime]: the RuntimeDB guard holds on every acquire, and the
// runtime role has no privileged attribute or membership.
func TestRuntime_Precondition(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	ctx := testCtx(t)
	env := fixture.LoadEnv(t)
	pool := fixture.RuntimeDB(t)

	// The four privileged roles exist, so a false pg_has_role is meaningful.
	privileged := []string{env.OwnerRole(), env.MigratorRole(), env.RetentionRole(), env.PolicyReaderRole()}
	requireEqual(t, "privileged roles present", count(t, ctx, pool, `SELECT count(*) FROM pg_roles WHERE rolname = ANY($1)`, privileged), 4)

	for i := 0; i < 3; i++ { // every acquire is guarded; observe it repeatedly
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		var sessionUser, currentUser string
		var super, bypass, createRole, createDB, selfMember bool
		err = conn.QueryRow(ctx, `
			SELECT session_user::text, current_user::text, r.rolsuper, r.rolbypassrls, r.rolcreaterole, r.rolcreatedb,
			       pg_has_role(current_user, current_user, 'MEMBER')
			FROM pg_roles r WHERE r.rolname = current_user`).
			Scan(&sessionUser, &currentUser, &super, &bypass, &createRole, &createDB, &selfMember)
		if err != nil {
			conn.Release()
			t.Fatal(err)
		}
		requireEqual(t, "session_user", sessionUser, env.RuntimeRole())
		requireEqual(t, "current_user", currentUser, env.RuntimeRole())
		if super || bypass || createRole || createDB {
			t.Fatalf("runtime role privileged: super=%t bypassrls=%t createrole=%t createdb=%t", super, bypass, createRole, createDB)
		}
		requireEqual(t, "positive control: pg_has_role(self)", selfMember, true)
		for _, r := range privileged {
			var member bool
			if err := conn.QueryRow(ctx, `SELECT pg_has_role(current_user, $1, 'MEMBER')`, r).Scan(&member); err != nil {
				t.Fatal(err)
			}
			if member {
				t.Fatalf("runtime role is a member of %s", r)
			}
		}
		conn.Release()
	}
}

// Case 2 [Runtime]: every app table with organization_id has RLS enabled
// and forced (catches tables added later); there is no tenants table.
func TestRuntime_RLSCoverage(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	ctx := testCtx(t)
	pool := fixture.RuntimeDB(t)

	rows, err := pool.Query(ctx, `
		SELECT c.relname::text, c.relrowsecurity, c.relforcerowsecurity
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'app' AND c.relkind IN ('r', 'p')
		  AND (c.relname = 'organizations' OR EXISTS (
		        SELECT 1 FROM pg_attribute a
		        WHERE a.attrelid = c.oid AND a.attname = 'organization_id' AND NOT a.attisdropped))`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name string
		var enabled, forced bool
		if err := rows.Scan(&name, &enabled, &forced); err != nil {
			t.Fatal(err)
		}
		if !enabled || !forced {
			t.Errorf("app.%s: relrowsecurity=%t relforcerowsecurity=%t, want both true", name, enabled, forced)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	// Positive control: the scan finds every known tenant table.
	requireSet(t, "tenant tables found", names, "audit_events", "business_entities", "group_business_entities",
		"groups", "organization_policies", "organizations", "role_bindings", "stored_objects")

	requireEqual(t, "tables named tenants", count(t, ctx, pool, `
		SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = 'tenants' AND n.nspname NOT IN ('pg_catalog', 'information_schema')`), 0)
}

// Case 3 [Runtime]: in context A, GB1 is hidden (0 rows, no error); GA1 is
// visible and the A-only count is exact.
func TestRuntime_ReadIsolation(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	ctx := testCtx(t)
	pool := fixture.RuntimeDB(t)
	inTenant(t, ctx, pool, adaAlpha, func(tx pgx.Tx) {
		requireEqual(t, "GB1 rows in context A", count(t, ctx, tx, `SELECT count(*) FROM app.groups WHERE id = $1`, fixture.GroupGB1), 0)
		requireEqual(t, "control: GA1 visible", count(t, ctx, tx, `SELECT count(*) FROM app.groups WHERE id = $1`, fixture.GroupGA1), 1)
		requireEqual(t, "control: groups visible", count(t, ctx, tx, `SELECT count(*) FROM app.groups`), fixture.SeedGroupsAlpha)
		requireEqual(t, "control: groups in A", count(t, ctx, tx, `SELECT count(*) FROM app.groups WHERE organization_id = $1`, fixture.OrgAlpha), fixture.SeedGroupsAlpha)
		requireEqual(t, "BE-B rows in context A", count(t, ctx, tx, `SELECT count(*) FROM app.business_entities WHERE id = $1`, fixture.BusinessEntityB), 0)
		requireEqual(t, "control: business entities", count(t, ctx, tx, `SELECT count(*) FROM app.business_entities`), fixture.SeedBusinessEntitiesAlpha)
	})
}

// Case 4 [Runtime]: a forged organization_id on INSERT fails WITH CHECK.
func TestRuntime_ForgedInsert(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	ctx := testCtx(t)
	pool := fixture.RuntimeDB(t)
	inTenant(t, ctx, pool, adaAlpha, func(tx pgx.Tx) {
		requireRLSDenied(t, attempt(t, ctx, tx, `INSERT INTO app.groups (organization_id, slug, name) VALUES ($1, 'forged', 'Forged')`, fixture.OrgBravo))
		requireRLSDenied(t, attempt(t, ctx, tx, `INSERT INTO app.business_entities (organization_id, name) VALUES ($1, 'Forged')`, fixture.OrgBravo))
		// Positive control: the same insert for the active Organization works.
		requireEqual(t, "control: insert in A", mustExec(t, ctx, tx, `INSERT INTO app.groups (organization_id, slug, name) VALUES ($1, 'forged', 'Forged')`, fixture.OrgAlpha), int64(1))
	})
}

// Case 5 [Runtime]: a row cannot be moved to another Organization, and its
// id cannot change.
func TestRuntime_MoveRow(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	ctx := testCtx(t)
	pool := fixture.RuntimeDB(t)
	inTenant(t, ctx, pool, adaAlpha, func(tx pgx.Tx) {
		requireEqual(t, "control: update GA1 name", mustExec(t, ctx, tx, `UPDATE app.groups SET name = 'Renamed' WHERE id = $1`, fixture.GroupGA1), int64(1))

		err := attempt(t, ctx, tx, `UPDATE app.groups SET organization_id = $1 WHERE id = $2`, fixture.OrgBravo, fixture.GroupGA1)
		if err == nil {
			t.Fatal("moving GA1 to Organization B succeeded")
		}
		// Either line of defence is acceptable; both are named mechanisms.
		if !isState(err, stateIntegrity, "immutable") && !isState(err, stateInsufficientPrivilege, msgRLS) {
			t.Fatalf("moving GA1: want immutability trigger (23000) or WITH CHECK (42501), got %v", err)
		}
		requireState(t, attempt(t, ctx, tx, `UPDATE app.groups SET id = gen_random_uuid() WHERE id = $1`, fixture.GroupGA1), stateIntegrity, "immutable")
		requireState(t, attempt(t, ctx, tx, `UPDATE app.business_entities SET organization_id = $1 WHERE id = $2`, fixture.OrgBravo, fixture.BusinessEntityA), stateIntegrity, "immutable")
	})
}

// Case 6 [Runtime]: UPDATE and DELETE of a foreign row affect 0 rows with no
// error. (TestControl_ForeignRowUnchanged confirms GB1 is unchanged.)
func TestRuntime_ForeignUpdateDelete(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	ctx := testCtx(t)
	pool := fixture.RuntimeDB(t)
	inTenant(t, ctx, pool, adaAlpha, func(tx pgx.Tx) {
		requireEqual(t, "update GB1", mustExec(t, ctx, tx, `UPDATE app.groups SET name = 'Tampered' WHERE id = $1`, fixture.GroupGB1), int64(0))
		requireEqual(t, "delete GB1", mustExec(t, ctx, tx, `DELETE FROM app.groups WHERE id = $1`, fixture.GroupGB1), int64(0))
		requireEqual(t, "update BE-B", mustExec(t, ctx, tx, `UPDATE app.business_entities SET name = 'Tampered' WHERE id = $1`, fixture.BusinessEntityB), int64(0))
		requireEqual(t, "delete GB1 bindings", mustExec(t, ctx, tx, `DELETE FROM app.role_bindings WHERE organization_id = $1`, fixture.OrgBravo), int64(0))

		// Positive controls on the same connection.
		requireEqual(t, "control: update GA1", mustExec(t, ctx, tx, `UPDATE app.groups SET name = 'Renamed' WHERE id = $1`, fixture.GroupGA1), int64(1))
		mustExec(t, ctx, tx, `INSERT INTO app.groups (organization_id, slug, name) VALUES ($1, 'delete-probe', 'Probe')`, fixture.OrgAlpha)
		requireEqual(t, "control: delete own probe", mustExec(t, ctx, tx, `DELETE FROM app.groups WHERE slug = 'delete-probe'`), int64(1))
	})
}

// Case 7 [Runtime]: with no context, or an empty one, every tenant table and
// membership view returns 0 rows with no error, and INSERT fails WITH CHECK.
func TestRuntime_NoContext(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	ctx := testCtx(t)
	pool := fixture.RuntimeDB(t)

	readable := append(append([]string{"identities"}, tenantTables...), membershipViews...)
	check := func(tx pgx.Tx, label string) {
		for _, rel := range readable {
			requireEqual(t, label+": rows in "+rel, count(t, ctx, tx, `SELECT count(*) FROM `+ident(rel)), 0)
		}
		inserts := map[string][]any{
			`INSERT INTO app.groups (organization_id, slug, name) VALUES ($1, 'no-context', 'No context')`:                {fixture.OrgAlpha},
			`INSERT INTO app.business_entities (organization_id, name) VALUES ($1, 'No context')`:                         {fixture.OrgAlpha},
			`INSERT INTO app.group_business_entities (organization_id, group_id, business_entity_id) VALUES ($1, $2, $3)`: {fixture.OrgAlpha, fixture.GroupGA1, fixture.BusinessEntityA2},
			`INSERT INTO app.role_bindings (organization_id, subject_identity_id, scope_type, scope_id, role, source, group_id)
			 VALUES ($1, $2, 'group', $3, 'group_member', 'membership', $3)`: {fixture.OrgAlpha, fixture.IdentitySam, fixture.GroupGA1},
			`INSERT INTO app.stored_objects (id, organization_id, object_key, content_type, created_by)
			 VALUES ('e0000000-0000-4000-8000-000000000001', $1, 'org/a0000000-0000-4000-8000-000000000001/e0000000-0000-4000-8000-000000000001', 'text/plain', $2)`: {fixture.OrgAlpha, fixture.IdentityAda},
			`INSERT INTO app.audit_events (id, actor_type, actor_id, organization_id, resource_type, resource_id, action, outcome, trace_id, request_id)
			 VALUES (gen_random_uuid(), 'user', 'x', $1, 'group', 'x', 'group.update', 'allowed', 't', 'r')`: {fixture.OrgAlpha},
		}
		for sql, args := range inserts {
			err := attempt(t, ctx, tx, sql, args...)
			if err == nil {
				t.Fatalf("%s: insert without context succeeded: %s", label, oneLine(sql))
			}
			requireRLSDenied(t, err)
		}
	}

	// No context at all: a fresh transaction with no settings applied.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	check(tx, "no context")
	_ = tx.Rollback(ctx)

	// Explicitly empty context.
	inTenant(t, ctx, pool, noTenant, func(tx pgx.Tx) { check(tx, "empty context") })

	// Positive control on the same pool: the same relations have rows in A.
	inTenant(t, ctx, pool, adaAlpha, func(tx pgx.Tx) {
		for _, rel := range readable {
			if count(t, ctx, tx, `SELECT count(*) FROM `+ident(rel)) == 0 {
				t.Fatalf("control: %s has no rows in context A", rel)
			}
		}
	})
}

// Case 8 [Runtime]: malformed context raises invalid input; no rows return.
func TestRuntime_MalformedContext(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	ctx := testCtx(t)
	pool := fixture.RuntimeDB(t)
	malformed := []fixture.Tenant{
		{OrganizationID: "not-a-uuid", UserID: fixture.IdentityAda},
		{OrganizationID: fixture.OrgAlpha, UserID: "a0000000' OR '1'='1"},
		{OrganizationID: fixture.OrgAlpha + "x", UserID: fixture.IdentityAda},
	}
	for _, tn := range malformed {
		for _, rel := range []string{"groups", "business_entities", "identities", "group_memberships"} {
			inTenant(t, ctx, pool, tn, func(tx pgx.Tx) {
				n, err := attemptQuery(t, ctx, tx, `SELECT * FROM `+ident(rel))
				if n != 0 {
					t.Fatalf("%+v: %s returned %d rows", tn, rel, n)
				}
				requireState(t, err, stateInvalidText, "")
			})
		}
	}
	inTenant(t, ctx, pool, adaAlpha, func(tx pgx.Tx) {
		requireEqual(t, "control: well-formed context", count(t, ctx, tx, `SELECT count(*) FROM app.groups`), fixture.SeedGroupsAlpha)
	})
}

// Case 9 [Runtime]: cross-Organization associations fail by FK or WITH CHECK.
func TestRuntime_CrossOrgAssociation(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	ctx := testCtx(t)
	pool := fixture.RuntimeDB(t)
	const insert = `INSERT INTO app.group_business_entities (organization_id, group_id, business_entity_id) VALUES ($1, $2, $3)`
	inTenant(t, ctx, pool, adaAlpha, func(tx pgx.Tx) {
		requireState(t, attempt(t, ctx, tx, insert, fixture.OrgAlpha, fixture.GroupGA1, fixture.BusinessEntityB), stateForeignKey, "")
		requireRLSDenied(t, attempt(t, ctx, tx, insert, fixture.OrgBravo, fixture.GroupGB1, fixture.BusinessEntityB))
		requireEqual(t, "control: (A, GA1, BE-A) exists", count(t, ctx, tx,
			`SELECT count(*) FROM app.group_business_entities WHERE organization_id = $1 AND group_id = $2 AND business_entity_id = $3`,
			fixture.OrgAlpha, fixture.GroupGA1, fixture.BusinessEntityA), 1)
		requireEqual(t, "control: same-org link insert", mustExec(t, ctx, tx, insert, fixture.OrgAlpha, fixture.GroupGA1, fixture.BusinessEntityA2), int64(1))
	})
}

// Case 10 [Runtime]: a group membership binding cannot point at a foreign Group.
func TestRuntime_CrossOrgMembershipBinding(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	ctx := testCtx(t)
	pool := fixture.RuntimeDB(t)
	inTenant(t, ctx, pool, adaAlpha, func(tx pgx.Tx) {
		requireState(t, attempt(t, ctx, tx, `
			INSERT INTO app.role_bindings (organization_id, subject_identity_id, scope_type, scope_id, role, source, group_id)
			VALUES ($1, $2, 'group', $3, 'group_member', 'membership', $3)`,
			fixture.OrgAlpha, fixture.IdentityLin, fixture.GroupGB1), stateForeignKey, "")
		requireEqual(t, "control: lin's GA1 binding", count(t, ctx, tx, `
			SELECT count(*) FROM app.role_bindings
			WHERE subject_identity_id = $1 AND scope_type = 'group' AND group_id = $2 AND source = 'membership'`,
			fixture.IdentityLin, fixture.GroupGA1), 1)
		requireEqual(t, "control: projected in group_memberships", count(t, ctx, tx,
			`SELECT count(*) FROM app.group_memberships WHERE user_id = $1 AND group_id = $2 AND role = 'group_admin'`,
			fixture.IdentityLin, fixture.GroupGA1), 1)
	})
}

// Case 11 [Runtime]: group scope through the narrowly scoped definer helpers.
func TestRuntime_GroupScope(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	ctx := testCtx(t)
	pool := fixture.RuntimeDB(t)
	cases := []struct {
		name                 string
		tn                   fixture.Tenant
		groups, bes, mGroups []string
	}{
		{"lin", linAlpha, []string{fixture.GroupGA1}, []string{fixture.BusinessEntityA}, []string{fixture.GroupGA1}},
		{"sam", samAlpha, []string{fixture.GroupGA2}, []string{fixture.BusinessEntityA, fixture.BusinessEntityA2}, []string{fixture.GroupGA2}},
		{"ada", adaAlpha, []string{fixture.GroupGA1, fixture.GroupGA2}, []string{fixture.BusinessEntityA, fixture.BusinessEntityA2}, []string{fixture.GroupGA1, fixture.GroupGA2}},
		{"kai", kaiAlpha, nil, []string{fixture.BusinessEntityA}, nil},
	}
	for _, c := range cases {
		inTenant(t, ctx, pool, c.tn, func(tx pgx.Tx) {
			requireSet(t, c.name+": groups", ids(t, ctx, tx, `SELECT id::text FROM app.groups`), c.groups...)
			requireSet(t, c.name+": business entities", ids(t, ctx, tx, `SELECT id::text FROM app.business_entities`), c.bes...)
			requireSet(t, c.name+": group_memberships groups", ids(t, ctx, tx, `SELECT DISTINCT group_id::text FROM app.group_memberships`), c.mGroups...)
		})
	}
}

// Case 12 [Runtime]: on a single pooled connection, context never survives
// a commit, a rollback or an error. (TestControl_SessionSetConfigLeaks shows
// the unsafe session-level alternative would leak.)
func TestRuntime_PooledReuse(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	ctx := testCtx(t)
	pool := fixture.RuntimeDBSingle(t)
	var pid0 int
	if err := pool.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid0); err != nil {
		t.Fatal(err)
	}
	assertClean := func(path string) {
		t.Helper()
		var pid int
		var org, user *string
		var groups int
		if err := pool.QueryRow(ctx, `
			SELECT pg_backend_pid(), current_setting('app.organization_id', true), current_setting('app.user_id', true),
			       (SELECT count(*) FROM app.groups)`).Scan(&pid, &org, &user, &groups); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		requireEqual(t, path+": backend pid", pid, pid0)
		if org == nil || *org != "" || user == nil || *user != "" {
			t.Fatalf("%s: context leaked: organization=%v user=%v", path, deref(org), deref(user))
		}
		requireEqual(t, path+": groups without context", groups, 0)
	}
	inContext := func(tx pgx.Tx) {
		var pid int
		if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		requireEqual(t, "backend pid in context", pid, pid0)
		requireEqual(t, "control: groups in context", count(t, ctx, tx, `SELECT count(*) FROM app.groups`), fixture.SeedGroupsAlpha)
	}

	// Commit path (WithTenantTx is the pattern consumers copy).
	if err := fixture.WithTenantTx(ctx, pool, adaAlpha, func(tx pgx.Tx) error { inContext(tx); return nil }); err != nil {
		t.Fatal(err)
	}
	assertClean("after commit")

	// Rollback path.
	if err := fixture.InTenantRollback(ctx, pool, adaAlpha, func(tx pgx.Tx) error { inContext(tx); return nil }); err != nil {
		t.Fatal(err)
	}
	assertClean("after rollback")

	// Error path: a statement fails inside the transaction.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.SetTenant(ctx, tx, adaAlpha); err != nil {
		t.Fatal(err)
	}
	inContext(tx)
	_, err = tx.Exec(ctx, `INSERT INTO app.groups (organization_id, slug, name) VALUES ($1, 'err-path', 'Err')`, fixture.OrgBravo)
	requireRLSDenied(t, err)
	_ = tx.Rollback(ctx)
	assertClean("after error")
}

func deref(s *string) string {
	if s == nil {
		return "<NULL>"
	}
	return *s
}

// Case 13 [Runtime]: the runtime role may only INSERT audit events, and only
// for the active Organization (or with no Organization before context).
func TestRuntime_AuditPrivileges(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	ctx := testCtx(t)
	pool := fixture.RuntimeDB(t)
	const insert = `INSERT INTO app.audit_events (id, actor_type, actor_id, organization_id, resource_type, resource_id, action, outcome, trace_id, request_id)
		VALUES (gen_random_uuid(), 'user', $2, $1, 'group', $3, 'group.update', 'allowed', 'trace-13', 'req-13')`
	inTenant(t, ctx, pool, adaAlpha, func(tx pgx.Tx) {
		requireEqual(t, "control: valid insert", mustExec(t, ctx, tx, insert, fixture.OrgAlpha, fixture.IdentityAda, fixture.GroupGA1), int64(1))
		requireRLSDenied(t, attempt(t, ctx, tx, insert, fixture.OrgBravo, fixture.IdentityAda, fixture.GroupGB1))
		requirePermissionDenied(t, attempt(t, ctx, tx, `UPDATE app.audit_events SET outcome = 'denied'`))
		requirePermissionDenied(t, attempt(t, ctx, tx, `DELETE FROM app.audit_events`))
		requirePermissionDenied(t, attempt(t, ctx, tx, `TRUNCATE app.audit_events`))
		_, err := attemptQuery(t, ctx, tx, `SELECT id FROM app.audit_events`)
		requirePermissionDenied(t, err)
		// occurred_at is server time: the runtime role cannot backdate events
		// into the retention window (column-level INSERT grant).
		requirePermissionDenied(t, attempt(t, ctx, tx, `INSERT INTO app.audit_events
			(id, occurred_at, actor_type, actor_id, organization_id, resource_type, resource_id, action, outcome, trace_id, request_id)
			VALUES (gen_random_uuid(), now() - interval '1000 days', 'user', $2, $1, 'group', $3, 'group.update', 'allowed', 't', 'r')`,
			fixture.OrgAlpha, fixture.IdentityAda, fixture.GroupGA1))
	})
}

// Stored-object keys are derived from their own row: a metadata row in A can
// never point at an object key of B (CHECK stored_objects_key_derived).
func TestRuntime_ObjectKeyBoundToRow(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	ctx := testCtx(t)
	pool := fixture.RuntimeDB(t)
	const insert = `INSERT INTO app.stored_objects (id, organization_id, object_key, content_type, created_by) VALUES ($1, $2, $3, 'text/plain', $4)`
	const id = "e0000000-0000-4000-8000-000000000002"
	inTenant(t, ctx, pool, adaAlpha, func(tx pgx.Tx) {
		requireEqual(t, "control: derived key", mustExec(t, ctx, tx, insert, id, fixture.OrgAlpha, "org/"+fixture.OrgAlpha+"/"+id, fixture.IdentityAda), int64(1))
		pgErr := requireState(t, attempt(t, ctx, tx, insert, "e0000000-0000-4000-8000-000000000003", fixture.OrgAlpha,
			"org/"+fixture.OrgBravo+"/"+fixture.ObjectBravoGB1, fixture.IdentityAda), stateCheck, "")
		requireEqual(t, "key constraint", pgErr.ConstraintName, "stored_objects_key_derived")
	})
}

// Audit attribution (plan §2.6 audit cases): each event category carries the
// attribution fields; Organization may be null only before context exists.
func TestRuntime_AuditAttribution(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	ctx := testCtx(t)
	pool := fixture.RuntimeDB(t)
	const insert = `INSERT INTO app.audit_events
		(id, actor_type, actor_id, organization_id, group_id, resource_type, resource_id, action, outcome, trace_id, request_id, detail)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`

	// Security denial before any context: Organization is null.
	inTenant(t, ctx, pool, noTenant, func(tx pgx.Tx) {
		requireEqual(t, "pre-context security event", mustExec(t, ctx, tx, insert,
			"user", fixture.IdentityAda, nil, nil, "session", "anonymous", "auth.denied", "denied", "trace-a", "req-a", map[string]any{"reason": "no_session"}), int64(1))
		requireRLSDenied(t, attempt(t, ctx, tx, insert,
			"user", fixture.IdentityAda, fixture.OrgAlpha, nil, "session", "x", "auth.denied", "denied", "trace-a", "req-a", map[string]any{}))
	})

	inTenant(t, ctx, pool, adaAlpha, func(tx pgx.Tx) {
		// Context change.
		requireEqual(t, "context change", mustExec(t, ctx, tx, insert,
			"user", fixture.IdentityAda, fixture.OrgAlpha, fixture.GroupGA1, "group", fixture.GroupGA1, "context.switch", "allowed", "trace-b", "req-b", map[string]any{}), int64(1))
		// Membership / role change, atomically with the single binding write.
		requireEqual(t, "role change write", mustExec(t, ctx, tx,
			`UPDATE app.role_bindings SET role = 'group_member' WHERE subject_identity_id = $1 AND group_id = $2`, fixture.IdentityLin, fixture.GroupGA1), int64(1))
		requireEqual(t, "role change event", mustExec(t, ctx, tx, insert,
			"user", fixture.IdentityAda, fixture.OrgAlpha, fixture.GroupGA1, "role_binding", fixture.IdentityLin, "role_binding.update", "allowed", "trace-c", "req-c",
			map[string]any{"from": "group_admin", "to": "group_member"}), int64(1))
		// Administrative mutation: a Group slug update.
		requireEqual(t, "slug update", mustExec(t, ctx, tx, `UPDATE app.groups SET slug = 'ga-one-renamed' WHERE id = $1`, fixture.GroupGA1), int64(1))
		requireEqual(t, "admin mutation event", mustExec(t, ctx, tx, insert,
			"user", fixture.IdentityAda, fixture.OrgAlpha, fixture.GroupGA1, "group", fixture.GroupGA1, "group.update", "allowed", "trace-d", "req-d",
			map[string]any{"field": "slug"}), int64(1))
		// Attribution is mandatory: a missing trace id is rejected.
		requireState(t, attempt(t, ctx, tx, insert,
			"user", fixture.IdentityAda, fixture.OrgAlpha, nil, "group", fixture.GroupGA1, "group.update", "allowed", nil, "req-e", map[string]any{}), stateNotNull, "")
		requireState(t, attempt(t, ctx, tx, insert,
			"robot", fixture.IdentityAda, fixture.OrgAlpha, nil, "group", fixture.GroupGA1, "group.update", "allowed", "t", "r", map[string]any{}), stateCheck, "")
	})
}

// Case 15 [Runtime]: role switching is denied.
func TestRuntime_RoleSwitching(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	ctx := testCtx(t)
	env := fixture.LoadEnv(t)
	pool := fixture.RuntimeDB(t)
	inTenant(t, ctx, pool, noTenant, func(tx pgx.Tx) {
		for _, r := range []string{env.OwnerRole(), env.MigratorRole(), env.RetentionRole(), env.PolicyReaderRole(), "postgres"} {
			requireState(t, attempt(t, ctx, tx, `SET ROLE `+pgx.Identifier{r}.Sanitize()), stateInsufficientPrivilege, "permission denied to set role")
			requireState(t, attempt(t, ctx, tx, `SET SESSION AUTHORIZATION `+pgx.Identifier{r}.Sanitize()), stateInsufficientPrivilege, "permission denied to set session authorization")
		}
		var cu string
		if err := tx.QueryRow(ctx, `SELECT current_user::text`).Scan(&cu); err != nil {
			t.Fatal(err)
		}
		requireEqual(t, "control: still runtime", cu, env.RuntimeRole())
	})
}

// Case 16 [Runtime]: protections cannot be disabled by the runtime role.
func TestRuntime_DisableProtections(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	ctx := testCtx(t)
	pool := fixture.RuntimeDB(t)
	inTenant(t, ctx, pool, adaAlpha, func(tx pgx.Tx) {
		for _, sql := range []string{
			`ALTER TABLE app.groups DISABLE ROW LEVEL SECURITY`,
			`ALTER TABLE app.groups NO FORCE ROW LEVEL SECURITY`,
			`ALTER TABLE app.audit_events NO FORCE ROW LEVEL SECURITY`,
			`DROP POLICY groups_runtime_select ON app.groups`,
			`ALTER TABLE app.audit_events DISABLE TRIGGER audit_events_no_update_delete`,
			`ALTER TABLE app.groups DISABLE TRIGGER ALL`,
		} {
			requireState(t, attempt(t, ctx, tx, sql), stateInsufficientPrivilege, msgMustBeOwner)
		}
		requireEqual(t, "control: groups still protected", count(t, ctx, tx,
			`SELECT count(*) FROM pg_class WHERE oid = 'app.groups'::regclass AND relrowsecurity AND relforcerowsecurity`), 1)
		requireEqual(t, "control: groups in context", count(t, ctx, tx, `SELECT count(*) FROM app.groups`), fixture.SeedGroupsAlpha)
	})
}

// Case 17 [Runtime]: row_security = off raises instead of bypassing RLS.
func TestRuntime_RowSecurityOff(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	ctx := testCtx(t)
	pool := fixture.RuntimeDB(t)
	inTenant(t, ctx, pool, adaAlpha, func(tx pgx.Tx) {
		requireEqual(t, "control: row_security on", count(t, ctx, tx, `SELECT count(*) FROM app.groups`), fixture.SeedGroupsAlpha)
		mustExec(t, ctx, tx, `SET LOCAL row_security = off`)
		n, err := attemptQuery(t, ctx, tx, `SELECT id FROM app.groups`)
		requireEqual(t, "rows with row_security off", n, 0)
		requireState(t, err, stateInsufficientPrivilege, "row-level security policy")
	})
}

// Case 18 [Runtime]: the runtime role cannot create objects.
func TestRuntime_ObjectCreation(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	ctx := testCtx(t)
	pool := fixture.RuntimeDB(t)
	inTenant(t, ctx, pool, noTenant, func(tx pgx.Tx) {
		for _, schema := range []string{"public", "app"} {
			for _, sql := range []string{
				`CREATE TABLE ` + schema + `.probe_table (id int)`,
				`CREATE FUNCTION ` + schema + `.probe_fn() RETURNS int LANGUAGE sql AS 'SELECT 1'`,
				`CREATE VIEW ` + schema + `.probe_view AS SELECT 1 AS x`,
			} {
				requireState(t, attempt(t, ctx, tx, sql), stateInsufficientPrivilege, "permission denied for schema "+schema)
			}
		}
		requireState(t, attempt(t, ctx, tx, `CREATE TEMP TABLE probe_temp (id int)`), stateInsufficientPrivilege, "permission denied to create temporary tables")
		requireEqual(t, "control: query works", count(t, ctx, tx, `SELECT 1`), 1)
	})
}

// definerAllowlist is the exact set of SECURITY DEFINER functions in app.
var definerAllowlist = []string{"is_org_admin", "my_business_entity_ids", "my_group_ids", "my_organization_ids"}

// Case 19 [Runtime]: SECURITY DEFINER functions equal the allowlist and are
// narrowly configured; every view uses security_invoker.
func TestRuntime_DefinerAllowlist(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	ctx := testCtx(t)
	env := fixture.LoadEnv(t)
	pool := fixture.RuntimeDB(t)

	rows, err := pool.Query(ctx, `
		SELECT p.proname::text, pg_get_userbyid(p.proowner)::text, r.rolcanlogin, r.rolbypassrls, r.rolsuper,
		       p.provolatile::text, coalesce(p.proconfig, '{}'),
		       ARRAY(SELECT CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE pg_get_userbyid(a.grantee)::text END
		             FROM aclexplode(coalesce(p.proacl, acldefault('f', p.proowner))) a
		             WHERE a.privilege_type = 'EXECUTE' AND a.grantee <> p.proowner ORDER BY 1)
		FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace
		JOIN pg_roles r ON r.oid = p.proowner
		WHERE n.nspname = 'app' AND p.prosecdef`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name, owner, volatile string
		var login, bypass, super bool
		var config, executors []string
		if err := rows.Scan(&name, &owner, &login, &bypass, &super, &volatile, &config, &executors); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
		if owner != env.PolicyReaderRole() || login || bypass || super {
			t.Errorf("%s: owner %s (login=%t bypassrls=%t super=%t), want NOLOGIN NOBYPASSRLS %s", name, owner, login, bypass, super, env.PolicyReaderRole())
		}
		if !slices.ContainsFunc(config, func(s string) bool { return strings.HasPrefix(s, "search_path=") }) {
			t.Errorf("%s: search_path not pinned (proconfig %v)", name, config)
		}
		if volatile != "s" {
			t.Errorf("%s: provolatile %q, want STABLE", name, volatile)
		}
		if !slices.Equal(executors, []string{env.RuntimeRole()}) {
			t.Errorf("%s: EXECUTE granted to %v, want only %s", name, executors, env.RuntimeRole())
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	requireSet(t, "SECURITY DEFINER functions in app", names, definerAllowlist...)

	// Every view in app uses security_invoker.
	views := ids(t, ctx, pool, `
		SELECT c.relname::text FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'app' AND c.relkind IN ('v', 'm')`)
	invoker := ids(t, ctx, pool, `
		SELECT c.relname::text FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'app' AND c.relkind = 'v'
		  AND EXISTS (SELECT 1 FROM unnest(c.reloptions) o WHERE lower(o) IN ('security_invoker=true', 'security_invoker=on', 'security_invoker=1'))`)
	requireSet(t, "views (control: all three found)", views, membershipViews...)
	requireSet(t, "views with security_invoker", invoker, views...)
}

// Case 20 [Runtime]: identities are visible only through shared
// Organization membership; own memberships across Organizations show only
// the caller's rows; no credential-like columns exist.
//
// The seed has no identity that belongs only to B, so the case is paired the
// other way round: lin, sam and kai belong only to A. The same identity row
// is visible in context A (positive control) and hidden in context B.
func TestRuntime_IdentitiesAndMemberships(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	ctx := testCtx(t)
	pool := fixture.RuntimeDB(t)
	inTenant(t, ctx, pool, adaAlpha, func(tx pgx.Tx) {
		// Positive control: co-members of A are visible; the B-only identity is not.
		requireSet(t, "ada in A: identities", ids(t, ctx, tx, `SELECT id::text FROM app.identities`),
			fixture.IdentityAda, fixture.IdentityLin, fixture.IdentitySam, fixture.IdentityKai)
		requireEqual(t, "B-only identity in A", count(t, ctx, tx, `SELECT count(*) FROM app.identities WHERE id = $1`, fixture.IdentityBea), 0)
	})
	inTenant(t, ctx, pool, adaBravo, func(tx pgx.Tx) {
		// In B ada is a plain org_member: she sees herself only (members do not
		// see other members' bindings), and never an A-only identity.
		requireSet(t, "ada in B: identities", ids(t, ctx, tx, `SELECT id::text FROM app.identities`), fixture.IdentityAda)
		requireEqual(t, "A-only identity in B", count(t, ctx, tx, `SELECT count(*) FROM app.identities WHERE id = $1`, fixture.IdentityLin), 0)
		// Own memberships across Organizations: only the caller's rows.
		requireSet(t, "ada's organization memberships", ids(t, ctx, tx, `SELECT organization_id::text FROM app.organization_memberships`),
			fixture.OrgAlpha, fixture.OrgBravo)
		requireEqual(t, "foreign rows in own memberships", count(t, ctx, tx,
			`SELECT count(*) FROM app.organization_memberships WHERE user_id <> $1`, fixture.IdentityAda), 0)
	})
	inTenant(t, ctx, pool, linAlpha, func(tx pgx.Tx) {
		requireSet(t, "lin in A: identities", ids(t, ctx, tx, `SELECT id::text FROM app.identities`), fixture.IdentityLin)
		requireEqual(t, "lin's organization memberships", count(t, ctx, tx, `SELECT count(*) FROM app.organization_memberships`), 0)
		requireSet(t, "lin's group memberships", ids(t, ctx, tx, `SELECT user_id::text FROM app.group_memberships`), fixture.IdentityLin)
	})

	credential := regexp.MustCompile(`(?i)(pass|secret|token|credential|hash|salt|api_?key|private)`)
	cols := ids(t, ctx, pool, `
		SELECT c.relname || '.' || a.attname FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'app' AND c.relkind IN ('r', 'p', 'v') AND a.attnum > 0 AND NOT a.attisdropped`)
	if !slices.Contains(cols, "identities.email") {
		t.Fatalf("control: column scan did not find identities.email (%d columns)", len(cols))
	}
	for _, c := range cols {
		if credential.MatchString(c) {
			t.Errorf("credential-like column %s", c)
		}
	}
}

// Case 21 [Runtime]: slug rules. Slugs grant nothing and leak nothing.
func TestRuntime_Slugs(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	ctx := testCtx(t)
	pool := fixture.RuntimeDB(t)
	const insert = `INSERT INTO app.groups (organization_id, slug, name) VALUES ($1, $2, 'Slug probe')`
	inTenant(t, ctx, pool, adaAlpha, func(tx pgx.Tx) {
		// A slug that exists only in B can be used in A: no existence oracle.
		requireEqual(t, "B-only slug in A", mustExec(t, ctx, tx, insert, fixture.OrgAlpha, "gb-one"), int64(1))
		pgErr := requireState(t, attempt(t, ctx, tx, insert, fixture.OrgAlpha, "ga-one"), stateUnique, "")
		requireEqual(t, "unique constraint", pgErr.ConstraintName, "groups_org_slug_unique")
		pgErr = requireState(t, attempt(t, ctx, tx, insert, fixture.OrgAlpha, "settings"), stateCheck, "")
		requireEqual(t, "reserved constraint", pgErr.ConstraintName, "groups_slug_reserved")
		for _, bad := range []string{"Bad_Slug", "-lead", "trail-", "double--dash", "has space", "UPPER"} {
			pgErr = requireState(t, attempt(t, ctx, tx, insert, fixture.OrgAlpha, bad), stateCheck, "")
			requireEqual(t, "format constraint for "+bad, pgErr.ConstraintName, "groups_slug_format")
		}
		requireEqual(t, "slug update", mustExec(t, ctx, tx, `UPDATE app.groups SET slug = 'ga-one-renamed' WHERE id = $1`, fixture.GroupGA1), int64(1))
		requireSet(t, "UUID unchanged after slug update", ids(t, ctx, tx, `SELECT id::text FROM app.groups WHERE slug = 'ga-one-renamed'`), fixture.GroupGA1)
	})
}

// Case 24 [Runtime]: role_bindings is the single mutable authority store;
// membership views are read-only projections; one row change propagates.
// (TestControl_SingleRowChange confirms no other table changes. The policy
// evaluator's BusinessEntity capabilities over the same change are asserted
// by the policy package's PostgreSQL-backed store test.)
func TestRuntime_AuthoritySingleStore(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	ctx := testCtx(t)
	pool := fixture.RuntimeDB(t)

	// Catalog: role_bindings is the only table with a role column.
	rows, err := pool.Query(ctx, `
		SELECT c.relname::text, c.relkind::text FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_attribute a ON a.attrelid = c.oid AND a.attname = 'role' AND NOT a.attisdropped
		WHERE n.nspname = 'app' AND c.relkind IN ('r', 'p', 'v', 'm', 'f') -- data-bearing relations, not indexes
		ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			t.Fatal(err)
		}
		kinds[name] = kind
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"role_bindings": "r", "organization_memberships": "v", "group_memberships": "v", "business_entity_memberships": "v"}
	if len(kinds) != len(want) {
		t.Fatalf("relations with a role column: %v, want %v", kinds, want)
	}
	for k, v := range want {
		requireEqual(t, "relkind of "+k, kinds[k], v)
	}
	requireEqual(t, "membership views with security_invoker", count(t, ctx, pool, `
		SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'app' AND c.relkind = 'v' AND c.relname = ANY($1)
		  AND 'security_invoker=true' = ANY(c.reloptions)`, membershipViews), len(membershipViews))

	inTenant(t, ctx, pool, adaAlpha, func(tx pgx.Tx) {
		// Read-only projections: writes through them are missing privileges.
		for _, v := range membershipViews {
			requireEqual(t, "control: "+v+" readable", count(t, ctx, tx, `SELECT count(*) FROM `+ident(v)) > 0, true)
			requirePermissionDenied(t, attempt(t, ctx, tx, `UPDATE `+ident(v)+` SET role = role`))
			requirePermissionDenied(t, attempt(t, ctx, tx, `DELETE FROM `+ident(v)))
		}
		requirePermissionDenied(t, attempt(t, ctx, tx, `INSERT INTO app.organization_memberships (user_id, organization_id, role) VALUES ($1, $2, 'org_admin')`, fixture.IdentityLin, fixture.OrgAlpha))
		requirePermissionDenied(t, attempt(t, ctx, tx, `INSERT INTO app.group_memberships (user_id, organization_id, group_id, role) VALUES ($1, $2, $3, 'group_admin')`, fixture.IdentitySam, fixture.OrgAlpha, fixture.GroupGA1))
		requirePermissionDenied(t, attempt(t, ctx, tx, `INSERT INTO app.business_entity_memberships (user_id, organization_id, business_entity_id, role, source) VALUES ($1, $2, $3, 'be_owner', 'direct_grant')`, fixture.IdentityLin, fixture.OrgAlpha, fixture.BusinessEntityA2))

		bindingsBefore := count(t, ctx, tx, `SELECT count(*) FROM app.role_bindings`)
		observeLin := func(step, wantRole string, wantGroups, wantBEs []string) {
			t.Helper()
			var role string
			err := tx.QueryRow(ctx, `SELECT coalesce(max(role), '') FROM app.group_memberships WHERE user_id = $1 AND group_id = $2`, fixture.IdentityLin, fixture.GroupGA1).Scan(&role)
			if err != nil {
				t.Fatal(err)
			}
			requireEqual(t, step+": lin's GA1 membership role", role, wantRole)
			// Same transaction, lin's own context: RLS visibility follows.
			if err := fixture.SetTenant(ctx, tx, linAlpha); err != nil {
				t.Fatal(err)
			}
			requireSet(t, step+": lin's groups", ids(t, ctx, tx, `SELECT id::text FROM app.groups`), wantGroups...)
			requireSet(t, step+": lin's business entities", ids(t, ctx, tx, `SELECT id::text FROM app.business_entities`), wantBEs...)
			if err := fixture.SetTenant(ctx, tx, adaAlpha); err != nil {
				t.Fatal(err)
			}
		}
		observeLin("before", "group_admin", []string{fixture.GroupGA1}, []string{fixture.BusinessEntityA})

		requireEqual(t, "downgrade is one row", mustExec(t, ctx, tx,
			`UPDATE app.role_bindings SET role = 'group_member' WHERE subject_identity_id = $1 AND scope_type = 'group' AND group_id = $2 AND source = 'membership'`,
			fixture.IdentityLin, fixture.GroupGA1), int64(1))
		observeLin("after downgrade", "group_member", []string{fixture.GroupGA1}, []string{fixture.BusinessEntityA})
		requireEqual(t, "binding count after downgrade", count(t, ctx, tx, `SELECT count(*) FROM app.role_bindings`), bindingsBefore)

		requireEqual(t, "revoke is one row", mustExec(t, ctx, tx,
			`DELETE FROM app.role_bindings WHERE subject_identity_id = $1 AND scope_type = 'group' AND group_id = $2 AND source = 'membership'`,
			fixture.IdentityLin, fixture.GroupGA1), int64(1))
		observeLin("after revoke", "", nil, nil)
		requireEqual(t, "binding count after revoke", count(t, ctx, tx, `SELECT count(*) FROM app.role_bindings`), bindingsBefore-1)
	})
}
