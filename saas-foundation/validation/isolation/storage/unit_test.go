package storage

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"example.invalid/archetype/isolation/internal/fixture"
)

// offlineStore presigns against a placeholder endpoint; presigning never
// touches the network.
func offlineStore(t *testing.T) *S3Store {
	t.Helper()
	s, err := NewS3Store(S3Config{Endpoint: "http://objects.invalid:9000", Region: "us-east-1", Bucket: "unit-bucket",
		AccessKey: "unit-access", SecretKey: "unit-secret-not-real"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// recordingSigner wraps a signer and records the keys it was asked to sign.
type recordingSigner struct {
	inner Signer
	keys  []string
}

func (r *recordingSigner) SignedGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	r.keys = append(r.keys, key)
	return r.inner.SignedGetURL(ctx, key, ttl)
}

// failingDB records Begin calls and refuses them.
type failingDB struct{ begins int }

func (f *failingDB) Begin(context.Context) (pgx.Tx, error) {
	f.begins++
	return nil, errors.New("unit: no database")
}

var seededAlphaKey = "org/" + fixture.OrgAlpha + "/" + fixture.ObjectAlphaGA1

func TestUnit_KeyValidation(t *testing.T) {
	fixture.Label(t, fixture.Unit)
	key, err := NewObjectKey(fixture.OrgAlpha, fixture.ObjectAlphaGA1)
	if err != nil || key != seededAlphaKey {
		t.Fatalf("NewObjectKey = %q, %v; want %q", key, err, seededAlphaKey)
	}
	if err := ValidateKey(key); err != nil {
		t.Fatalf("control: server key rejected: %v", err)
	}
	if id := NewObjectID(); !IsUUID(id) {
		t.Fatalf("NewObjectID %q is not a canonical UUID", id)
	}
	bad := []string{
		"",
		"report.pdf",
		"/" + seededAlphaKey,
		seededAlphaKey + "/",
		"org//" + fixture.ObjectAlphaGA1,
		"org/" + fixture.OrgAlpha + "/../" + fixture.ObjectBravoGB1,
		"org/" + fixture.OrgAlpha + "/./" + fixture.ObjectAlphaGA1,
		"org/" + fixture.OrgAlpha + "/..",
		"org%2F" + fixture.OrgAlpha + "%2F" + fixture.ObjectAlphaGA1,
		"org/" + fixture.OrgAlpha + "%2f" + fixture.ObjectAlphaGA1,
		"org/" + fixture.OrgAlpha + "%2e%2e/" + fixture.ObjectAlphaGA1,
		"org\\" + fixture.OrgAlpha + "\\" + fixture.ObjectAlphaGA1,
		"org/" + strings.ToUpper(fixture.OrgAlpha) + "/" + fixture.ObjectAlphaGA1,
		"org/" + fixture.OrgAlpha + "/" + fixture.ObjectAlphaGA1 + "\x00",
		"org/" + fixture.OrgAlpha + "/" + fixture.ObjectAlphaGA1 + "?x=1",
		"tenant/" + fixture.OrgAlpha + "/" + fixture.ObjectAlphaGA1,
	}
	for _, k := range bad {
		if err := ValidateKey(k); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("ValidateKey(%q) = %v, want ErrInvalidKey", k, err)
		}
	}
	if _, err := NewObjectKey("../x", fixture.ObjectAlphaGA1); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("NewObjectKey with traversal organization = %v", err)
	}
	// The adapter refuses invalid keys before any provider call.
	s := offlineStore(t)
	if _, err := s.SignedGetURL(context.Background(), "../"+seededAlphaKey, time.Minute); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("adapter signed an invalid key: %v", err)
	}
}

func TestUnit_TTLCap(t *testing.T) {
	fixture.Label(t, fixture.Unit)
	a := &Authorizer{}
	for _, c := range []struct{ in, want time.Duration }{
		{0, DefaultSignedURLTTL},
		{-time.Second, DefaultSignedURLTTL},
		{30 * time.Second, 30 * time.Second},
		{MaxSignedURLTTL, MaxSignedURLTTL},
		{24 * time.Hour, MaxSignedURLTTL},
	} {
		if got := a.EffectiveTTL(c.in); got != c.want {
			t.Errorf("EffectiveTTL(%s) = %s, want %s", c.in, got, c.want)
		}
	}
	if got := (&Authorizer{MaxTTL: time.Hour}).EffectiveTTL(time.Hour); got != MaxSignedURLTTL {
		t.Errorf("configured MaxTTL above the hard cap: got %s", got)
	}
	s := offlineStore(t)
	ctx := context.Background()
	u, err := s.SignedGetURL(ctx, seededAlphaKey, a.EffectiveTTL(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Query().Get("X-Amz-Expires"); got != "900" {
		t.Errorf("X-Amz-Expires = %q, want 900", got)
	}
	if !strings.HasSuffix(parsed.Path, "/unit-bucket/"+seededAlphaKey) {
		t.Errorf("path-style URL path = %q", parsed.Path)
	}
	if _, err := s.SignedGetURL(ctx, seededAlphaKey, MaxSignedURLTTL+time.Second); !errors.Is(err, ErrInvalidTTL) {
		t.Errorf("adapter accepted a TTL above the cap: %v", err)
	}
}

// Client-supplied keys, traversal and encoded slashes are rejected before
// any database or store access.
func TestUnit_RejectsClientReferences(t *testing.T) {
	fixture.Label(t, fixture.Unit)
	db := &failingDB{}
	signer := &recordingSigner{inner: offlineStore(t)}
	a := &Authorizer{DB: db, Signer: signer}
	caller := Caller{IdentityID: fixture.IdentityAda, OrganizationID: fixture.OrgAlpha}
	for _, ref := range []string{
		seededAlphaKey,
		"org/" + fixture.OrgAlpha + "/" + NewObjectID(),
		"../" + fixture.ObjectBravoGB1,
		fixture.ObjectAlphaGA1 + "/../" + fixture.ObjectBravoGB1,
		"org%2F" + fixture.OrgAlpha + "%2F" + fixture.ObjectAlphaGA1,
		strings.ToUpper(fixture.ObjectAlphaGA1),
		"",
	} {
		_, err := a.SignedGetURL(context.Background(), caller, Request{ObjectID: ref})
		if !errors.Is(err, ErrInvalidReference) {
			t.Errorf("reference %q: got %v, want ErrInvalidReference", ref, err)
		}
	}
	if len(signer.keys) != 0 {
		t.Fatalf("signer was called for rejected references: %v", signer.keys)
	}
	// Only the denial audit attempted a transaction; no lookup ran.
	if db.begins != 7 {
		t.Fatalf("database begins = %d, want one denial-audit attempt per reference (7)", db.begins)
	}
	// A malformed server-side caller is refused outright.
	if _, err := a.SignedGetURL(context.Background(), Caller{IdentityID: "x", OrganizationID: fixture.OrgAlpha}, Request{ObjectID: fixture.ObjectAlphaGA1}); !errors.Is(err, ErrInvalidCaller) {
		t.Fatalf("malformed caller: %v", err)
	}
}
