package policy_test

import (
	"crypto/rand"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"example.invalid/archetype/isolation/internal/fixture"
	"example.invalid/archetype/isolation/policy"
)

func newUUID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func count(t *testing.T, tx pgx.Tx, query string, args ...any) int {
	t.Helper()
	var n int
	if err := tx.QueryRow(ctx, query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// TestRuntime_RevocationSingleRowChange: revoking lin's GA1 binding is one
// role_bindings row; in the same transaction it changes the group_memberships
// projection and lin's effective BusinessEntity access (PGStore-backed
// evaluator), with no mirrored rows, and the audit event is atomic with it.
func TestRuntime_RevocationSingleRowChange(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	pool := fixture.RuntimeDB(t)
	admin := fixture.Tenant{OrganizationID: fixture.OrgAlpha, UserID: fixture.IdentityAda}
	linCtx := fixture.Tenant{OrganizationID: fixture.OrgAlpha, UserID: fixture.IdentityLin}
	linTC := policy.TenantContext{IdentityID: fixture.IdentityLin, OrganizationID: fixture.OrgAlpha}
	auditID := newUUID(t)
	var revokedBindingID string

	err := fixture.InTenantRollback(ctx, pool, linCtx, func(tx pgx.Tx) error {
		e := policy.NewEvaluator(policy.NewPGStore(tx))

		// Positive control as lin under lin's own context: the inherited
		// profile update is allowed and the resolver accepts alpha/GA1.
		if err := e.Authorize(ctx, linTC, policy.CapBusinessEntityProfileUpdate, be(fixture.BusinessEntityA)); err != nil {
			t.Fatalf("before revocation (lin context): %v", err)
		}
		if _, err := policy.NewResolver(policy.NewPGStore(tx), nil).Resolve(ctx, valid(fixture.IdentityLin), fixture.OrgAlpha, fixture.GroupGA1); err != nil {
			t.Fatalf("before revocation: resolve lin GA1: %v", err)
		}

		// Switch to the administrator's context (transaction-local).
		if err := fixture.SetTenant(ctx, tx, admin); err != nil {
			return err
		}
		if err := e.Authorize(ctx, linTC, policy.CapBusinessEntityProfileUpdate, be(fixture.BusinessEntityA)); err != nil {
			t.Fatalf("before revocation (admin context): %v", err)
		}
		totalBefore := count(t, tx, `SELECT count(*) FROM app.role_bindings`)
		linRows := count(t, tx, `SELECT count(*) FROM app.role_bindings WHERE subject_identity_id = $1`, fixture.IdentityLin)
		if linRows != 1 {
			t.Fatalf("lin has %d role_bindings rows, want exactly 1 (the single authority row)", linRows)
		}
		if n := count(t, tx, `SELECT count(*) FROM app.group_memberships WHERE user_id = $1 AND group_id = $2`, fixture.IdentityLin, fixture.GroupGA1); n != 1 {
			t.Fatalf("group_memberships lists lin in GA1 %d times before revocation, want 1", n)
		}
		// No mirrored authority anywhere else: no BusinessEntity grant for lin.
		if n := count(t, tx, `SELECT count(*) FROM app.business_entity_memberships WHERE user_id = $1`, fixture.IdentityLin); n != 0 {
			t.Fatalf("lin has %d mirrored business_entity_memberships rows", n)
		}
		assocBefore := count(t, tx, `SELECT count(*) FROM app.group_business_entities`)

		// The revocation: exactly one row.
		if err := tx.QueryRow(ctx, `SELECT id::text FROM app.role_bindings WHERE subject_identity_id = $1 AND group_id = $2`, fixture.IdentityLin, fixture.GroupGA1).Scan(&revokedBindingID); err != nil {
			t.Fatalf("find lin binding: %v", err)
		}
		tag, err := tx.Exec(ctx, `DELETE FROM app.role_bindings WHERE id = $1`, revokedBindingID)
		if err != nil {
			t.Fatalf("revoke: %v", err)
		}
		if tag.RowsAffected() != 1 {
			t.Fatalf("revocation affected %d rows, want 1", tag.RowsAffected())
		}
		if after := count(t, tx, `SELECT count(*) FROM app.role_bindings`); after != totalBefore-1 {
			t.Fatalf("role_bindings count %d -> %d, want a change of exactly 1", totalBefore, after)
		}
		if n := count(t, tx, `SELECT count(*) FROM app.role_bindings WHERE subject_identity_id = $1`, fixture.IdentityLin); n != 0 {
			t.Fatalf("lin still has %d authority rows after one delete", n)
		}
		if n := count(t, tx, `SELECT count(*) FROM app.group_business_entities`); n != assocBefore {
			t.Fatalf("associations changed %d -> %d; revocation must not touch them", assocBefore, n)
		}

		// Same transaction: the projection and effective access follow.
		if n := count(t, tx, `SELECT count(*) FROM app.group_memberships WHERE user_id = $1 AND group_id = $2`, fixture.IdentityLin, fixture.GroupGA1); n != 0 {
			t.Fatalf("group_memberships still lists lin in GA1 after revocation")
		}
		if err := e.Authorize(ctx, linTC, policy.CapBusinessEntityProfileUpdate, be(fixture.BusinessEntityA)); err != policy.ErrNotFound {
			t.Fatalf("after revocation (admin context): %v, want ErrNotFound", err)
		}
		// Ada's own access is untouched (control on the same connection).
		adaTC := policy.TenantContext{IdentityID: fixture.IdentityAda, OrganizationID: fixture.OrgAlpha}
		if err := e.Authorize(ctx, adaTC, policy.CapBusinessEntityProfileUpdate, be(fixture.BusinessEntityA)); err != nil {
			t.Fatalf("ada after revocation: %v", err)
		}

		// The audit event commits (or rolls back) with the revocation.
		if _, err := tx.Exec(ctx, `
			INSERT INTO app.audit_events (id, actor_type, actor_id, organization_id, group_id, resource_type, resource_id, action, outcome, trace_id, request_id)
			VALUES ($1, 'user', $2, $3, $4, 'role_binding', $5, 'membership.revoke', 'allowed', 'trace-revocation', 'request-revocation')`,
			auditID, fixture.IdentityAda, fixture.OrgAlpha, fixture.GroupGA1, revokedBindingID); err != nil {
			t.Fatalf("audit insert: %v", err)
		}

		// And under lin's own context in the same transaction.
		if err := fixture.SetTenant(ctx, tx, linCtx); err != nil {
			return err
		}
		if err := e.Authorize(ctx, linTC, policy.CapBusinessEntityProfileUpdate, be(fixture.BusinessEntityA)); err != policy.ErrNotFound {
			t.Fatalf("after revocation (lin context): %v, want ErrNotFound", err)
		}
		if _, err := policy.NewResolver(policy.NewPGStore(tx), nil).Resolve(ctx, valid(fixture.IdentityLin), fixture.OrgAlpha, ""); err != policy.ErrNotFound {
			t.Fatalf("after revocation lin still resolves alpha: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Atomicity: the rollback removed both the revocation and the audit
	// event. The runtime role cannot read audit_events, so re-inserting the
	// same primary key proves the first insert is gone.
	err = fixture.InTenantRollback(ctx, pool, admin, func(tx pgx.Tx) error {
		if n := count(t, tx, `SELECT count(*) FROM app.role_bindings WHERE id = $1`, revokedBindingID); n != 1 {
			t.Fatalf("binding not restored by rollback (count %d)", n)
		}
		if n := count(t, tx, `SELECT count(*) FROM app.group_memberships WHERE user_id = $1 AND group_id = $2`, fixture.IdentityLin, fixture.GroupGA1); n != 1 {
			t.Fatalf("projection not restored by rollback")
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO app.audit_events (id, actor_type, actor_id, organization_id, resource_type, resource_id, action, outcome, trace_id, request_id)
			VALUES ($1, 'user', $2, $3, 'role_binding', $4, 'membership.revoke', 'allowed', 'trace-recheck', 'request-recheck')`,
			auditID, fixture.IdentityAda, fixture.OrgAlpha, revokedBindingID)
		if err != nil {
			t.Fatalf("audit event survived the rollback (re-insert failed): %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
