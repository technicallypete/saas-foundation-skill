package rls

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"example.invalid/archetype/isolation/internal/fixture"
)

// SQLSTATE codes asserted by the cases.
const (
	stateInsufficientPrivilege = "42501" // RLS WITH CHECK, missing privilege, "must be owner", audit trigger
	stateForeignKey            = "23503"
	stateUnique                = "23505"
	stateCheck                 = "23514"
	stateNotNull               = "23502"
	stateIntegrity             = "23000" // immutability trigger
	stateInvalidText           = "22P02" // malformed uuid context
)

// Message fragments that distinguish mechanisms sharing SQLSTATE 42501.
const (
	msgRLS         = "row-level security"
	msgPermission  = "permission denied"
	msgMustBeOwner = "must be owner"
	msgAppendOnly  = "append-only"
)

// Seeded audit events (sql/40_seed.sql): one expired and one recent event
// per Organization.
const (
	auditAlphaExpired = "d0000000-0000-4000-8000-000000000001"
	auditAlphaRecent  = "d0000000-0000-4000-8000-000000000002"
	auditBravoExpired = "d0000000-0000-4000-8000-000000000003"
	auditBravoRecent  = "d0000000-0000-4000-8000-000000000004"
)

var seededAuditIDs = []string{auditAlphaExpired, auditAlphaRecent, auditBravoExpired, auditBravoRecent}

// Server-resolved tenant contexts used by the cases.
var (
	adaAlpha = fixture.Tenant{OrganizationID: fixture.OrgAlpha, UserID: fixture.IdentityAda}
	adaBravo = fixture.Tenant{OrganizationID: fixture.OrgBravo, UserID: fixture.IdentityAda}
	linAlpha = fixture.Tenant{OrganizationID: fixture.OrgAlpha, UserID: fixture.IdentityLin}
	samAlpha = fixture.Tenant{OrganizationID: fixture.OrgAlpha, UserID: fixture.IdentitySam}
	kaiAlpha = fixture.Tenant{OrganizationID: fixture.OrgAlpha, UserID: fixture.IdentityKai}
	noTenant = fixture.Tenant{}
)

// tenantTables are the tables carrying organization_id (plus the
// Organization table itself); membershipViews are the derived projections.
var (
	tenantTables = []string{"organizations", "organization_policies", "groups", "business_entities",
		"group_business_entities", "role_bindings", "stored_objects"}
	membershipViews = []string{"organization_memberships", "group_memberships", "business_entity_memberships"}
)

type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// count returns a single integer result.
func count(t *testing.T, ctx context.Context, q querier, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := q.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", oneLine(sql), err)
	}
	return n
}

// ids returns the sorted first-column text values of a query.
func ids(t *testing.T, ctx context.Context, q querier, sql string, args ...any) []string {
	t.Helper()
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		t.Fatalf("%s: %v", oneLine(sql), err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("%s: %v", oneLine(sql), err)
	}
	slices.Sort(out)
	return out
}

// mustExec runs a statement expected to succeed and returns rows affected.
func mustExec(t *testing.T, ctx context.Context, q querier, sql string, args ...any) int64 {
	t.Helper()
	tag, err := q.Exec(ctx, sql, args...)
	if err != nil {
		t.Fatalf("%s: %v", oneLine(sql), err)
	}
	return tag.RowsAffected()
}

// attempt runs a statement inside a savepoint that is always rolled back,
// so an expected failure does not abort the surrounding transaction.
func attempt(t *testing.T, ctx context.Context, tx pgx.Tx, sql string, args ...any) error {
	t.Helper()
	sp, err := tx.Begin(ctx)
	if err != nil {
		t.Fatalf("savepoint: %v", err)
	}
	_, execErr := sp.Exec(ctx, sql, args...)
	if err := sp.Rollback(ctx); err != nil {
		t.Fatalf("rollback to savepoint: %v", err)
	}
	return execErr
}

// attemptQuery runs a query inside a rolled-back savepoint and returns the
// number of rows delivered before any error.
func attemptQuery(t *testing.T, ctx context.Context, tx pgx.Tx, sql string, args ...any) (int, error) {
	t.Helper()
	sp, err := tx.Begin(ctx)
	if err != nil {
		t.Fatalf("savepoint: %v", err)
	}
	defer func() { _ = sp.Rollback(ctx) }()
	rows, err := sp.Query(ctx, sql, args...)
	if err != nil {
		return 0, err
	}
	n := 0
	for rows.Next() {
		n++
	}
	rows.Close()
	return n, rows.Err()
}

// requireState asserts the SQLSTATE and, when given, a message fragment.
func requireState(t *testing.T, err error, code, msgFragment string) *pgconn.PgError {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("want SQLSTATE %s (%q), got %v", code, msgFragment, err)
	}
	if pgErr.Code != code {
		t.Fatalf("want SQLSTATE %s (%q), got %s: %s", code, msgFragment, pgErr.Code, pgErr.Message)
	}
	if msgFragment != "" && !strings.Contains(pgErr.Message, msgFragment) {
		t.Fatalf("SQLSTATE %s: want message containing %q, got %q", code, msgFragment, pgErr.Message)
	}
	return pgErr
}

// requireRLSDenied asserts a WITH CHECK violation (not a missing grant).
func requireRLSDenied(t *testing.T, err error) {
	t.Helper()
	requireState(t, err, stateInsufficientPrivilege, msgRLS)
}

// requirePermissionDenied asserts a missing privilege (not RLS).
func requirePermissionDenied(t *testing.T, err error) {
	t.Helper()
	pgErr := requireState(t, err, stateInsufficientPrivilege, msgPermission)
	if strings.Contains(pgErr.Message, msgRLS) {
		t.Fatalf("expected a privilege denial, got an RLS denial: %s", pgErr.Message)
	}
}

func requireSet(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	w := slices.Clone(want)
	slices.Sort(w)
	if !slices.Equal(got, w) {
		t.Fatalf("%s: got %v, want %v", what, got, w)
	}
}

func requireEqual[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %v, want %v", what, got, want)
	}
}

// inTenant runs fn in a rolled-back transaction with the given context.
func inTenant(t *testing.T, ctx context.Context, pool interface {
	Begin(context.Context) (pgx.Tx, error)
}, tn fixture.Tenant, fn func(tx pgx.Tx)) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fixture.SetTenant(ctx, tx, tn); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	fn(tx)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func ident(s string) string { return pgx.Identifier{"app", s}.Sanitize() }

func isState(err error, code, msgFragment string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code && strings.Contains(pgErr.Message, msgFragment)
}
