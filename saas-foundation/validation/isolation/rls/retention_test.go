package rls

import (
	"testing"

	"example.invalid/archetype/isolation/internal/fixture"
)

// Case 14 [Retention]: the retention role deletes exactly the expired row it
// targets; it cannot touch rows inside the window, update, insert, or read
// event content.
func TestRetention_AuditRetention(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	ctx := testCtx(t)
	pool := fixture.RetentionDB(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Retention sees only expired rows, and only id and occurred_at.
	requireSet(t, "visible seeded events", ids(t, ctx, tx, `SELECT id::text FROM app.audit_events WHERE id = ANY($1::uuid[])`, seededAuditIDs),
		auditAlphaExpired, auditBravoExpired)
	requireEqual(t, "visible rows inside the window", count(t, ctx, tx,
		`SELECT count(*) FROM app.audit_events WHERE occurred_at >= now() - make_interval(days => $1)`, fixture.AuditRetentionDays), 0)

	// Positive control: delete one expired event; exactly that row goes.
	requireEqual(t, "delete expired event", mustExec(t, ctx, tx, `DELETE FROM app.audit_events WHERE id = $1`, auditAlphaExpired), int64(1))
	requireSet(t, "seeded events after delete", ids(t, ctx, tx, `SELECT id::text FROM app.audit_events WHERE id = ANY($1::uuid[])`, seededAuditIDs),
		auditBravoExpired)

	// Inside the window: hidden by the retention policy, 0 rows, no error.
	requireEqual(t, "delete recent event", mustExec(t, ctx, tx, `DELETE FROM app.audit_events WHERE id = $1`, auditAlphaRecent), int64(0))
	requireEqual(t, "delete all recent events", mustExec(t, ctx, tx,
		`DELETE FROM app.audit_events WHERE occurred_at >= now() - make_interval(days => $1)`, fixture.AuditRetentionDays), int64(0))
	// Second line of defence: the append-only trigger is installed and enabled.
	requireEqual(t, "append-only triggers enabled", count(t, ctx, tx, `
		SELECT count(*) FROM pg_trigger
		WHERE tgrelid = 'app.audit_events'::regclass AND NOT tgisinternal AND tgenabled = 'O'`), 2)

	requirePermissionDenied(t, attempt(t, ctx, tx, `UPDATE app.audit_events SET occurred_at = now() WHERE id = $1`, auditBravoExpired))
	requirePermissionDenied(t, attempt(t, ctx, tx, `
		INSERT INTO app.audit_events (id, actor_type, actor_id, resource_type, resource_id, action, outcome, trace_id, request_id)
		VALUES (gen_random_uuid(), 'system', 'retention', 'audit', 'x', 'audit.forge', 'allowed', 't', 'r')`))
	for _, col := range []string{"actor_id", "organization_id", "action", "detail", "*"} {
		_, err := attemptQuery(t, ctx, tx, `SELECT `+col+` FROM app.audit_events`)
		requirePermissionDenied(t, err)
	}
	requireEqual(t, "control: id and occurred_at readable", count(t, ctx, tx,
		`SELECT count(*) FROM (SELECT id, occurred_at FROM app.audit_events WHERE id = $1) s`, auditBravoExpired), 1)
}
