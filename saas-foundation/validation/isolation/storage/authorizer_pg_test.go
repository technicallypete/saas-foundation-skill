package storage

import (
	"context"
	"errors"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"example.invalid/archetype/isolation/internal/fixture"
)

// Real/PG authorizer tests: the stored_objects row is looked up through the
// runtime role under RLS. Each test runs inside one outer transaction that
// is rolled back; the Authorizer's own transactions are savepoints in it.

type recordingAudit struct {
	inner  AuditWriter
	events []AuditEvent
}

func (r *recordingAudit) Write(ctx context.Context, tx pgx.Tx, e AuditEvent) error {
	if err := r.inner.Write(ctx, tx, e); err != nil {
		return err
	}
	r.events = append(r.events, e)
	return nil
}

type failingAudit struct{}

func (failingAudit) Write(context.Context, pgx.Tx, AuditEvent) error {
	return errors.New("audit sink unavailable")
}

type denyAll struct{}

func (denyAll) CanReadObject(context.Context, pgx.Tx, Caller, ObjectRecord) (bool, error) {
	return false, nil
}

func callerOf(tn fixture.Tenant) Caller {
	return Caller{IdentityID: tn.UserID, OrganizationID: tn.OrganizationID, GroupID: tn.GroupID}
}

var (
	adaAlpha = fixture.Tenant{OrganizationID: fixture.OrgAlpha, UserID: fixture.IdentityAda}
	adaBravo = fixture.Tenant{OrganizationID: fixture.OrgBravo, UserID: fixture.IdentityAda}
	linAlpha = fixture.Tenant{OrganizationID: fixture.OrgAlpha, UserID: fixture.IdentityLin}
	samAlpha = fixture.Tenant{OrganizationID: fixture.OrgAlpha, UserID: fixture.IdentitySam}
	kaiAlpha = fixture.Tenant{OrganizationID: fixture.OrgAlpha, UserID: fixture.IdentityKai}
)

// withRuntimeTx gives fn an outer runtime transaction that is rolled back.
func withRuntimeTx(t *testing.T, fn func(ctx context.Context, tx pgx.Tx)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := fixture.RuntimeDB(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	fn(ctx, tx)
}

func visibleObjects(t *testing.T, ctx context.Context, tx pgx.Tx, tn fixture.Tenant, id string) int {
	t.Helper()
	if err := setTenant(ctx, tx, callerOf(tn)); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.stored_objects WHERE id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Issuing requires a row visible under RLS AND the scope's read capability.
func TestRuntime_SignedURLRequiresVisibilityAndCapability(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	withRuntimeTx(t, func(ctx context.Context, tx pgx.Tx) {
		signer := &recordingSigner{inner: offlineStore(t)}
		a := &Authorizer{DB: tx, Signer: signer}

		// Positive controls: the Organization owner and the GA1 admin.
		for _, tn := range []fixture.Tenant{adaAlpha, linAlpha} {
			got, err := a.SignedGetURL(ctx, callerOf(tn), Request{ObjectID: fixture.ObjectAlphaGA1, TraceID: "t", RequestID: "r"})
			if err != nil {
				t.Fatalf("%s: %v", tn.UserID, err)
			}
			if u, _ := url.Parse(got.URL); u == nil || u.Query().Get("X-Amz-Signature") == "" {
				t.Fatalf("not a signed URL: %q", got.URL)
			}
		}
		if len(signer.keys) != 2 || signer.keys[0] != seededAlphaKey {
			t.Fatalf("signed keys %v, want the seeded alpha key twice", signer.keys)
		}

		// (a) Hidden under RLS: sam is not in GA1.
		requireEqual(t, "GA1 object visible to sam", visibleObjects(t, ctx, tx, samAlpha, fixture.ObjectAlphaGA1), 0)
		if _, err := a.SignedGetURL(ctx, callerOf(samAlpha), Request{ObjectID: fixture.ObjectAlphaGA1}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("sam: got %v, want ErrNotFound", err)
		}

		// (b) Visible under RLS but no read capability: an Organization-scoped
		// object is visible to every caller in alpha, yet kai (a direct
		// BusinessEntity grant only) and sam (a Group member only) lack
		// organization.read.
		orgObject := NewObjectID()
		if err := setTenant(ctx, tx, callerOf(adaAlpha)); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO app.stored_objects (id, organization_id, object_key, content_type, created_by)
			VALUES ($1, $2, $3, 'text/plain', $4)`, orgObject, fixture.OrgAlpha, "org/"+fixture.OrgAlpha+"/"+orgObject, fixture.IdentityAda); err != nil {
			t.Fatal(err)
		}
		for _, tn := range []fixture.Tenant{kaiAlpha, samAlpha} {
			requireEqual(t, "org object visible under RLS to "+tn.UserID, visibleObjects(t, ctx, tx, tn, orgObject), 1)
			if _, err := a.SignedGetURL(ctx, callerOf(tn), Request{ObjectID: orgObject}); !errors.Is(err, ErrNotFound) {
				t.Fatalf("%s: got %v, want ErrNotFound", tn.UserID, err)
			}
		}
		if _, err := a.SignedGetURL(ctx, callerOf(adaAlpha), Request{ObjectID: orgObject}); err != nil {
			t.Fatalf("control: owner reads org object: %v", err)
		}

		// A capability check that denies overrides RLS visibility.
		strict := &Authorizer{DB: tx, Signer: signer, Capability: denyAll{}}
		if _, err := strict.SignedGetURL(ctx, callerOf(adaAlpha), Request{ObjectID: fixture.ObjectAlphaGA1}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("deny-all capability: got %v", err)
		}
		if len(signer.keys) != 3 {
			t.Fatalf("signer calls = %d, want 3 (denials must not sign)", len(signer.keys))
		}
	})
}

// A foreign object ID behaves exactly like a nonexistent one.
func TestRuntime_SignedURLForeignLikeNonexistent(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	withRuntimeTx(t, func(ctx context.Context, tx pgx.Tx) {
		signer := &recordingSigner{inner: offlineStore(t)}
		a := &Authorizer{DB: tx, Signer: signer}
		if _, err := a.SignedGetURL(ctx, callerOf(adaAlpha), Request{ObjectID: fixture.ObjectAlphaGA1}); err != nil {
			t.Fatalf("control: %v", err)
		}
		_, foreignErr := a.SignedGetURL(ctx, callerOf(adaAlpha), Request{ObjectID: fixture.ObjectBravoGB1})
		_, missingErr := a.SignedGetURL(ctx, callerOf(adaAlpha), Request{ObjectID: NewObjectID()})
		// ada is only an org_member in bravo and not in GB1.
		_, bravoErr := a.SignedGetURL(ctx, callerOf(adaBravo), Request{ObjectID: fixture.ObjectBravoGB1})
		for name, err := range map[string]error{"foreign": foreignErr, "nonexistent": missingErr, "bravo non-member": bravoErr} {
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("%s: got %v, want ErrNotFound", name, err)
			}
		}
		if foreignErr.Error() != missingErr.Error() || bravoErr.Error() != missingErr.Error() {
			t.Fatalf("denials are distinguishable: %q / %q / %q", foreignErr, missingErr, bravoErr)
		}
		if len(signer.keys) != 1 {
			t.Fatalf("signer calls = %d, want 1", len(signer.keys))
		}
	})
}

// A guessed key, or a guessed ID, under the caller's own prefix is denied.
func TestRuntime_SignedURLGuessedKeyDenied(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	withRuntimeTx(t, func(ctx context.Context, tx pgx.Tx) {
		signer := &recordingSigner{inner: offlineStore(t)}
		a := &Authorizer{DB: tx, Signer: signer}
		guessedID := NewObjectID()
		guessedKey, _ := NewObjectKey(fixture.OrgAlpha, guessedID)
		if _, err := a.SignedGetURL(ctx, callerOf(adaAlpha), Request{ObjectID: guessedKey}); !errors.Is(err, ErrInvalidReference) {
			t.Fatalf("guessed key: got %v, want ErrInvalidReference", err)
		}
		if _, err := a.SignedGetURL(ctx, callerOf(adaAlpha), Request{ObjectID: seededAlphaKey}); !errors.Is(err, ErrInvalidReference) {
			t.Fatalf("real key supplied by client: got %v, want ErrInvalidReference", err)
		}
		if _, err := a.SignedGetURL(ctx, callerOf(adaAlpha), Request{ObjectID: guessedID}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("guessed ID: got %v, want ErrNotFound", err)
		}
		if len(signer.keys) != 0 {
			t.Fatalf("signer called: %v", signer.keys)
		}
		if _, err := a.SignedGetURL(ctx, callerOf(adaAlpha), Request{ObjectID: fixture.ObjectAlphaGA1}); err != nil {
			t.Fatalf("control: %v", err)
		}
	})
}

// The TTL is capped and the URL carries the capped lifetime.
func TestRuntime_SignedURLTTLCapped(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	withRuntimeTx(t, func(ctx context.Context, tx pgx.Tx) {
		a := &Authorizer{DB: tx, Signer: offlineStore(t)}
		got, err := a.SignedGetURL(ctx, callerOf(adaAlpha), Request{ObjectID: fixture.ObjectAlphaGA1, TTL: 24 * time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(got.URL)
		if err != nil {
			t.Fatal(err)
		}
		requireEqual(t, "ExpiresIn", got.ExpiresIn, MaxSignedURLTTL)
		requireEqual(t, "X-Amz-Expires", u.Query().Get("X-Amz-Expires"), "900")
	})
}

// Issuing (and denying) is audited through the runtime connection; the URL
// is withheld when the audit write fails.
func TestRuntime_SignedURLAudited(t *testing.T) {
	fixture.Label(t, fixture.RealPG)
	withRuntimeTx(t, func(ctx context.Context, tx pgx.Tx) {
		audit := &recordingAudit{inner: PGAudit{}}
		a := &Authorizer{DB: tx, Signer: offlineStore(t), Audit: audit}
		got, err := a.SignedGetURL(ctx, callerOf(linAlpha), Request{ObjectID: fixture.ObjectAlphaGA1, TTL: time.Minute, TraceID: "trace-s", RequestID: "req-s"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.SignedGetURL(ctx, callerOf(samAlpha), Request{ObjectID: fixture.ObjectAlphaGA1, TraceID: "trace-d", RequestID: "req-d"}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("sam: %v", err)
		}
		// Both INSERTs were accepted by PostgreSQL (PGAudit checks one row).
		if len(audit.events) != 2 {
			t.Fatalf("audit events = %d, want 2", len(audit.events))
		}
		allowed, denied := audit.events[0], audit.events[1]
		want := AuditEvent{ActorID: fixture.IdentityLin, OrganizationID: fixture.OrgAlpha, GroupID: fixture.GroupGA1,
			ResourceID: fixture.ObjectAlphaGA1, Action: ActionSignGet, Outcome: "allowed", TraceID: "trace-s", RequestID: "req-s"}
		want.Detail = map[string]any{"ttl_seconds": 60}
		if !reflect.DeepEqual(allowed, want) {
			t.Fatalf("allowed event: got %+v, want %+v", allowed, want)
		}
		requireEqual(t, "denied outcome", denied.Outcome, "denied")
		requireEqual(t, "denied resource", denied.ResourceID, fixture.ObjectAlphaGA1)
		for _, ev := range audit.events {
			for k, v := range ev.Detail {
				if s, ok := v.(string); ok && s == got.URL {
					t.Fatalf("audit detail %q contains the signed URL", k)
				}
			}
		}

		// Fail closed: no URL without an audit record.
		failing := &Authorizer{DB: tx, Signer: offlineStore(t), Audit: failingAudit{}}
		got, err = failing.SignedGetURL(ctx, callerOf(adaAlpha), Request{ObjectID: fixture.ObjectAlphaGA1})
		if err == nil || got.URL != "" {
			t.Fatalf("URL issued without audit: %+v, %v", got, err)
		}
	})
}

func requireEqual[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %+v, want %+v", what, got, want)
	}
}
