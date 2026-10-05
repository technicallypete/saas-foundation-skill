package rls

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"example.invalid/archetype/isolation/internal/fixture"
)

// Case 2 [Owner]: FORCE RLS applies to the owner path. Authorized positive
// control, foreign-scope denial and no-context denial on one OwnerTx.
func TestOwner_ForcedRLS(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	fixture.OwnerTx(t, func(ctx context.Context, tx pgx.Tx) {
		requireEqual(t, "FORCE RLS on tables under test", count(t, ctx, tx, `
			SELECT count(*) FROM pg_class
			WHERE oid IN ('app.groups'::regclass, 'app.audit_events'::regclass) AND relrowsecurity AND relforcerowsecurity`), 2)

		if err := fixture.SetTenant(ctx, tx, fixture.Tenant{OrganizationID: fixture.OrgAlpha}); err != nil {
			t.Fatal(err)
		}
		// Authorized positive control: exactly A's seeded groups; insert in A.
		requireSet(t, "owner in A: groups", ids(t, ctx, tx, `SELECT id::text FROM app.groups`), fixture.GroupGA1, fixture.GroupGA2)
		requireEqual(t, "owner insert in A", mustExec(t, ctx, tx,
			`INSERT INTO app.groups (organization_id, slug, name) VALUES ($1, 'owner-probe', 'Owner probe')`, fixture.OrgAlpha), int64(1))

		// Foreign-scope denial in the same context.
		requireEqual(t, "owner in A: GB1 rows", count(t, ctx, tx, `SELECT count(*) FROM app.groups WHERE id = $1`, fixture.GroupGB1), 0)
		requireRLSDenied(t, attempt(t, ctx, tx,
			`INSERT INTO app.groups (organization_id, slug, name) VALUES ($1, 'owner-forged', 'Forged')`, fixture.OrgBravo))
		requireEqual(t, "owner update of GB1", mustExec(t, ctx, tx, `UPDATE app.groups SET name = 'Tampered' WHERE id = $1`, fixture.GroupGB1), int64(0))

		// No context: the owner sees no tenant rows.
		if err := fixture.SetTenant(ctx, tx, noTenant); err != nil {
			t.Fatal(err)
		}
		for _, rel := range append(append([]string{}, tenantTables...), "audit_events") {
			requireEqual(t, "owner without context: rows in "+rel, count(t, ctx, tx, `SELECT count(*) FROM `+ident(rel)), 0)
		}
	})
}

// Case 14 [Owner]: audit events on the owner path are Organization-bounded
// and append-only (the trigger blocks UPDATE, DELETE and TRUNCATE).
func TestOwner_AuditProtection(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	fixture.OwnerTx(t, func(ctx context.Context, tx pgx.Tx) {
		if err := fixture.SetTenant(ctx, tx, fixture.Tenant{OrganizationID: fixture.OrgAlpha}); err != nil {
			t.Fatal(err)
		}
		requireSet(t, "owner in A: seeded events", ids(t, ctx, tx,
			`SELECT id::text FROM app.audit_events WHERE id = ANY($1::uuid[])`, seededAuditIDs), auditAlphaExpired, auditAlphaRecent)
		requireEqual(t, "owner in A: foreign events", count(t, ctx, tx,
			`SELECT count(*) FROM app.audit_events WHERE organization_id IS DISTINCT FROM $1`, fixture.OrgAlpha), 0)

		requireState(t, attempt(t, ctx, tx, `UPDATE app.audit_events SET outcome = 'denied' WHERE id = $1`, auditAlphaRecent), stateInsufficientPrivilege, msgAppendOnly)
		requireState(t, attempt(t, ctx, tx, `DELETE FROM app.audit_events WHERE id = $1`, auditAlphaExpired), stateInsufficientPrivilege, msgAppendOnly)
		requireState(t, attempt(t, ctx, tx, `TRUNCATE app.audit_events`), stateInsufficientPrivilege, msgAppendOnly)

		// Control: the owner path may still append in its own Organization.
		requireEqual(t, "owner append in A", mustExec(t, ctx, tx, `
			INSERT INTO app.audit_events (id, actor_type, actor_id, organization_id, resource_type, resource_id, action, outcome, trace_id, request_id)
			VALUES (gen_random_uuid(), 'system', 'migration', $1, 'schema', 'app', 'schema.migrate', 'allowed', 't', 'r')`, fixture.OrgAlpha), int64(1))
	})
}
