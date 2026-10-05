// Package storage is the object-storage isolation reference: an ObjectStore
// port, an S3-compatible adapter (aws-sdk-go-v2, path-style addressing,
// offline presigning) and an Authorizer that issues short-lived signed URLs
// only for objects whose metadata row is visible under row-level security
// and whose owning scope grants read.
//
// Object keys are server-generated and opaque (org/<organization>/<object>).
// A key prefix confers nothing; authorization always starts from the
// stored_objects row, never from a client-supplied key.
package storage

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// ObjectInfo is object metadata returned by the store.
type ObjectInfo struct {
	Key         string
	Size        int64
	ContentType string
	ETag        string
}

// ObjectStore is the provider-neutral storage port. Implementations validate
// every key with ValidateKey before any provider call.
type ObjectStore interface {
	Put(ctx context.Context, key string, body []byte, contentType string) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
	// SignedGetURL returns a time-limited URL for exactly this key.
	SignedGetURL(ctx context.Context, key string, ttl time.Duration) (string, error)
	Metadata(ctx context.Context, key string) (ObjectInfo, error)
}

// Signed URL lifetimes. Requests above the cap are clamped by the
// Authorizer and refused by the adapter.
const (
	MaxSignedURLTTL     = 15 * time.Minute
	DefaultSignedURLTTL = 5 * time.Minute
)

var (
	// ErrInvalidKey reports a key that is not a server-generated opaque key.
	ErrInvalidKey = errors.New("storage: invalid object key")
	// ErrObjectMissing reports that the provider has no object at the key.
	ErrObjectMissing = errors.New("storage: object missing in store")
	// ErrInvalidTTL reports a signed-URL lifetime outside (0, MaxSignedURLTTL].
	ErrInvalidTTL = errors.New("storage: invalid signed URL lifetime")
)

const uuidPattern = `[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`

var (
	uuidRE = regexp.MustCompile(`^` + uuidPattern + `$`)
	// Only the exact server-generated shape is accepted. This rejects
	// traversal ("..", "./"), encoded separators ("%2F"), backslashes,
	// leading or doubled slashes and any client-chosen name.
	keyRE = regexp.MustCompile(`^org/` + uuidPattern + `/` + uuidPattern + `$`)
)

// IsUUID reports whether s is a canonical lowercase UUID.
func IsUUID(s string) bool { return uuidRE.MatchString(s) }

// NewObjectKey returns the server-generated opaque key of an object.
func NewObjectKey(organizationID, objectID string) (string, error) {
	if !IsUUID(organizationID) || !IsUUID(objectID) {
		return "", ErrInvalidKey
	}
	return "org/" + organizationID + "/" + objectID, nil
}

// ValidateKey accepts only server-generated opaque keys.
func ValidateKey(key string) error {
	if !keyRE.MatchString(key) {
		return ErrInvalidKey
	}
	return nil
}

// NewObjectID returns a random (version 4) UUID for a new object.
func NewObjectID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
