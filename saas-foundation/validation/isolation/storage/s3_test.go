package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"example.invalid/archetype/isolation/internal/fixture"
)

// Real/S3 tests run against the source-built MinIO with the scoped, non-root
// service credential. Without the settings they skip, unless the harness sets
// ARCHETYPE_REQUIRE_INTEGRATION=1 (then they fail), like fixture.LoadEnv.

func loadS3Config(t *testing.T) S3Config {
	t.Helper()
	cfg := S3Config{
		Endpoint:  os.Getenv("ARCHETYPE_S3_ENDPOINT"),
		Region:    os.Getenv("ARCHETYPE_S3_REGION"),
		Bucket:    os.Getenv("ARCHETYPE_S3_BUCKET"),
		AccessKey: os.Getenv("ARCHETYPE_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("ARCHETYPE_S3_SECRET_KEY"),
	}
	if cfg.Endpoint == "" || cfg.Region == "" || cfg.Bucket == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		if os.Getenv("ARCHETYPE_REQUIRE_INTEGRATION") == "1" {
			t.Fatal("object storage settings are missing but ARCHETYPE_REQUIRE_INTEGRATION=1")
		}
		t.Skip("object storage settings not provided (set by validation/run.sh)")
	}
	return cfg
}

type s3Fixture struct {
	store       *S3Store
	cfg         S3Config
	key         string // the seeded alpha object's key
	body        []byte
	siblingKey  string // same prefix, different object
	siblingBody []byte
}

// setupS3 uploads test bytes to the seeded alpha key and a sibling key in the
// same prefix with the scoped credential, and removes both afterwards.
func setupS3(t *testing.T) (context.Context, s3Fixture) {
	t.Helper()
	cfg := loadS3Config(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	store, err := NewS3Store(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sibling, _ := NewObjectKey(fixture.OrgAlpha, NewObjectID())
	f := s3Fixture{store: store, cfg: cfg, key: seededAlphaKey, body: []byte("alpha object " + NewObjectID()),
		siblingKey: sibling, siblingBody: []byte("sibling object " + NewObjectID())}
	for k, b := range map[string][]byte{f.key: f.body, f.siblingKey: f.siblingBody} {
		if err := store.Put(ctx, k, b, "text/plain"); err != nil {
			t.Fatalf("scoped credential put %s: %v", k, err)
		}
		key := k
		t.Cleanup(func() {
			if err := store.Delete(context.Background(), key); err != nil {
				t.Errorf("cleanup delete %s: %v", key, err)
			}
		})
	}
	return ctx, f
}

var httpClient = &http.Client{Timeout: 10 * time.Second}

func httpGet(t *testing.T, ctx context.Context, rawURL string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestS3_PresignedExactKey(t *testing.T) {
	fixture.Label(t, fixture.RealS3)
	ctx, f := setupS3(t)
	// Control: the scoped credential reads the object through the SDK.
	if got, err := f.store.Get(ctx, f.key); err != nil || !bytes.Equal(got, f.body) {
		t.Fatalf("scoped get: %q, %v", got, err)
	}
	if info, err := f.store.Metadata(ctx, f.key); err != nil || info.ContentType != "text/plain" || info.Size != int64(len(f.body)) {
		t.Fatalf("metadata: %+v, %v", info, err)
	}
	u, err := f.store.SignedGetURL(ctx, f.key, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	status, body := httpGet(t, ctx, u)
	if status != http.StatusOK || !bytes.Equal(body, f.body) {
		t.Fatalf("presigned GET: status %d body %q", status, body)
	}
}

func TestS3_SiblingKeySignatureFails(t *testing.T) {
	fixture.Label(t, fixture.RealS3)
	ctx, f := setupS3(t)
	signed, err := f.store.SignedGetURL(ctx, f.key, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	// Reuse the signature for the sibling object in the same prefix.
	u.Path = path.Join(path.Dir(u.Path), path.Base(f.siblingKey))
	status, body := httpGet(t, ctx, u.String())
	if status != http.StatusForbidden || !strings.Contains(string(body), "SignatureDoesNotMatch") {
		t.Fatalf("sibling with reused signature: status %d body %s", status, body)
	}
	// Control: the sibling exists and its own signed URL works.
	own, err := f.store.SignedGetURL(ctx, f.siblingKey, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if status, body := httpGet(t, ctx, own); status != http.StatusOK || !bytes.Equal(body, f.siblingBody) {
		t.Fatalf("control: sibling's own URL: status %d body %q", status, body)
	}
}

func TestS3_ExpiredURLFails(t *testing.T) {
	fixture.Label(t, fixture.RealS3)
	ctx, f := setupS3(t)
	u, err := f.store.SignedGetURL(ctx, f.key, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := httpGet(t, ctx, u); status != http.StatusOK {
		t.Fatalf("control: fresh URL status %d", status)
	}
	time.Sleep(4 * time.Second) // the expiry itself is under test; no condition to poll
	status, body := httpGet(t, ctx, u)
	if status != http.StatusForbidden || !strings.Contains(string(body), "AccessDenied") {
		t.Fatalf("expired URL: status %d body %s", status, body)
	}
}

func TestS3_AnonymousDenied(t *testing.T) {
	fixture.Label(t, fixture.RealS3)
	ctx, f := setupS3(t)
	base := strings.TrimRight(f.cfg.Endpoint, "/") + "/" + f.cfg.Bucket
	for name, u := range map[string]string{
		"anonymous GET":     base + "/" + f.key,
		"anonymous LIST":    base + "/",
		"anonymous LIST v2": base + "?list-type=2&prefix=org/",
	} {
		status, body := httpGet(t, ctx, u)
		if status != http.StatusForbidden || !strings.Contains(string(body), "AccessDenied") {
			t.Fatalf("%s: status %d body %s", name, status, body)
		}
	}
	// Control: the same object is reachable through a signed URL.
	signed, err := f.store.SignedGetURL(ctx, f.key, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := httpGet(t, ctx, signed); status != http.StatusOK {
		t.Fatalf("control: signed GET status %d", status)
	}
}

func TestS3_ScopedCredentialCannotAdministerBucket(t *testing.T) {
	fixture.Label(t, fixture.RealS3)
	ctx, f := setupS3(t)
	client := f.store.Client()
	// Control: the credential's granted bucket action works.
	if _, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(f.cfg.Bucket), Prefix: aws.String("org/")}); err != nil {
		t.Fatalf("control: list with scoped credential: %v", err)
	}
	publicRead := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["*"]},"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::` + f.cfg.Bucket + `/*"]}]}`
	_, err := client.PutBucketPolicy(ctx, &s3.PutBucketPolicyInput{Bucket: aws.String(f.cfg.Bucket), Policy: aws.String(publicRead)})
	requireAccessDenied(t, "PutBucketPolicy", err)
	_, err = client.DeleteBucketPolicy(ctx, &s3.DeleteBucketPolicyInput{Bucket: aws.String(f.cfg.Bucket)})
	requireAccessDenied(t, "DeleteBucketPolicy", err)
	_, err = client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(f.cfg.Bucket)})
	requireAccessDenied(t, "DeleteBucket", err)
	_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(f.cfg.Bucket + "-other")})
	requireAccessDenied(t, "CreateBucket", err)
}

func requireAccessDenied(t *testing.T, op string, err error) {
	t.Helper()
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "AccessDenied" {
		t.Fatalf("%s with scoped credential: want AccessDenied, got %v", op, err)
	}
}

// End to end: the Authorizer (RLS lookup through the runtime role) issues a
// URL that fetches exactly the authorized object from the real store.
func TestS3_AuthorizedSignedGet(t *testing.T) {
	fixture.Label(t, fixture.RealS3)
	ctx, f := setupS3(t)
	pool := fixture.RuntimeDB(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	a := &Authorizer{DB: tx, Signer: f.store}
	got, err := a.SignedGetURL(ctx, Caller{IdentityID: fixture.IdentityLin, OrganizationID: fixture.OrgAlpha},
		Request{ObjectID: fixture.ObjectAlphaGA1, TTL: time.Minute, TraceID: "trace-e2e", RequestID: "req-e2e"})
	if err != nil {
		t.Fatal(err)
	}
	if status, body := httpGet(t, ctx, got.URL); status != http.StatusOK || !bytes.Equal(body, f.body) {
		t.Fatalf("authorized GET: status %d body %q", status, body)
	}
	_, err = a.SignedGetURL(ctx, Caller{IdentityID: fixture.IdentitySam, OrganizationID: fixture.OrgAlpha},
		Request{ObjectID: fixture.ObjectAlphaGA1})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("sam: got %v, want ErrNotFound", err)
	}
}
