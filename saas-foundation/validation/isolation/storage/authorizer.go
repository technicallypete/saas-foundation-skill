package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	// ErrNotFound is returned for nonexistent, foreign, hidden and
	// unreadable objects alike, so a denial reveals nothing (no oracle).
	ErrNotFound = errors.New("storage: object not found")
	// ErrInvalidReference is returned when the caller supplies anything
	// other than an object ID (for example a key, a path or a traversal).
	ErrInvalidReference = errors.New("storage: invalid object reference")
	// ErrInvalidCaller is returned when the server-resolved caller context
	// is incomplete. It indicates a server bug, never a client input.
	ErrInvalidCaller = errors.New("storage: invalid caller context")
)

// Caller is the trusted, server-resolved request context. It is never built
// from client-supplied identifiers.
type Caller struct {
	IdentityID     string
	OrganizationID string
	GroupID        string // optional active Group
}

// Request asks for a signed GET URL for one object, referenced by ID only.
type Request struct {
	ObjectID  string        // client-supplied; must be an object ID, never a key
	TTL       time.Duration // 0 means DefaultSignedURLTTL; clamped to MaxTTL
	TraceID   string
	RequestID string
}

// SignedURL is an issued URL and its effective lifetime.
type SignedURL struct {
	URL       string
	ExpiresIn time.Duration
}

// ObjectRecord is the stored_objects row visible to the caller.
type ObjectRecord struct {
	ID             string
	OrganizationID string
	GroupID        string // empty for Organization-scoped objects
	Key            string
}

// Signer presigns a GET for one key (ObjectStore satisfies it).
type Signer interface {
	SignedGetURL(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// TxBeginner starts a transaction. *pgxpool.Pool (runtime role) satisfies it
// in production; tests pass an open pgx.Tx, whose Begin creates a savepoint
// so the outer test transaction can be rolled back.
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// ReadCapability decides storage.object.read: the read capability of the
// object's owning scope. The full policy evaluator can implement it; the
// default is MembershipReadCapability.
type ReadCapability interface {
	CanReadObject(ctx context.Context, tx pgx.Tx, caller Caller, obj ObjectRecord) (bool, error)
}

// AuditEvent is one attributed storage audit record.
type AuditEvent struct {
	ActorID        string
	OrganizationID string
	GroupID        string
	ResourceID     string
	Action         string
	Outcome        string // allowed | denied | error
	TraceID        string
	RequestID      string
	Detail         map[string]any
}

// AuditWriter records an audit event inside the given transaction.
type AuditWriter interface {
	Write(ctx context.Context, tx pgx.Tx, e AuditEvent) error
}

// Audit actions written by the Authorizer.
const (
	ActionSignGet = "storage.object.sign_get"
)

// Authorizer issues signed URLs. Issuing requires (a) the stored_objects row
// to be visible under RLS through the runtime role with the caller's tenant
// context, and (b) the owning scope's read capability. Every decision is
// audited; an allowed URL is returned only after its audit event commits.
type Authorizer struct {
	DB         TxBeginner
	Signer     Signer
	Capability ReadCapability // nil: MembershipReadCapability
	Audit      AuditWriter    // nil: PGAudit
	MaxTTL     time.Duration  // 0: MaxSignedURLTTL; never above it
}

// EffectiveTTL applies the default and the cap.
func (a *Authorizer) EffectiveTTL(requested time.Duration) time.Duration {
	limit := a.MaxTTL
	if limit <= 0 || limit > MaxSignedURLTTL {
		limit = MaxSignedURLTTL
	}
	if requested <= 0 {
		requested = DefaultSignedURLTTL
	}
	return min(requested, limit)
}

func (a *Authorizer) capability() ReadCapability {
	if a.Capability != nil {
		return a.Capability
	}
	return MembershipReadCapability{}
}

func (a *Authorizer) audit() AuditWriter {
	if a.Audit != nil {
		return a.Audit
	}
	return PGAudit{}
}

// SignedGetURL authorizes and issues a signed GET URL for req.ObjectID.
func (a *Authorizer) SignedGetURL(ctx context.Context, caller Caller, req Request) (SignedURL, error) {
	if !IsUUID(caller.IdentityID) || !IsUUID(caller.OrganizationID) || (caller.GroupID != "" && !IsUUID(caller.GroupID)) {
		return SignedURL{}, ErrInvalidCaller
	}
	ttl := a.EffectiveTTL(req.TTL)
	if !IsUUID(req.ObjectID) {
		// Keys, paths, traversal and encoded separators never reach the
		// database or the store.
		return SignedURL{}, a.deny(ctx, caller, req, "", ErrInvalidReference, "invalid_reference")
	}

	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return SignedURL{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := setTenant(ctx, tx, caller); err != nil {
		return SignedURL{}, err
	}

	var obj ObjectRecord
	err = tx.QueryRow(ctx, `
		SELECT id::text, organization_id::text, coalesce(group_id::text, ''), object_key
		FROM app.stored_objects WHERE id = $1`, req.ObjectID).
		Scan(&obj.ID, &obj.OrganizationID, &obj.GroupID, &obj.Key)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		return SignedURL{}, a.deny(ctx, caller, req, "", ErrNotFound, "not_visible")
	}
	if err != nil {
		return SignedURL{}, err
	}
	ok, err := a.capability().CanReadObject(ctx, tx, caller, obj)
	if err != nil {
		return SignedURL{}, err
	}
	if !ok || obj.OrganizationID != caller.OrganizationID {
		_ = tx.Rollback(ctx)
		return SignedURL{}, a.deny(ctx, caller, req, obj.GroupID, ErrNotFound, "no_read_capability")
	}
	// The stored key must be exactly the server-derived key for this row, so a
	// metadata row can never point at another Organization's object.
	expected, keyErr := NewObjectKey(obj.OrganizationID, obj.ID)
	if err := ValidateKey(obj.Key); err != nil || keyErr != nil || obj.Key != expected {
		_ = tx.Rollback(ctx)
		return SignedURL{}, a.deny(ctx, caller, req, obj.GroupID, ErrNotFound, "invalid_stored_key")
	}

	url, err := a.Signer.SignedGetURL(ctx, obj.Key, ttl)
	if err != nil {
		return SignedURL{}, err
	}
	// The URL is never written to the audit log; only the decision is.
	if err := a.audit().Write(ctx, tx, AuditEvent{
		ActorID: caller.IdentityID, OrganizationID: caller.OrganizationID, GroupID: obj.GroupID,
		ResourceID: obj.ID, Action: ActionSignGet, Outcome: "allowed",
		TraceID: req.TraceID, RequestID: req.RequestID,
		Detail: map[string]any{"ttl_seconds": int(ttl / time.Second)},
	}); err != nil {
		return SignedURL{}, fmt.Errorf("storage: audit failed, URL withheld: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return SignedURL{}, fmt.Errorf("storage: audit commit failed, URL withheld: %w", err)
	}
	return SignedURL{URL: url, ExpiresIn: ttl}, nil
}

// deny records a denial in its own transaction and returns the uniform
// denial error. The resource ID is recorded only when it is a well-formed
// object ID; client-supplied keys are never echoed into the audit log.
func (a *Authorizer) deny(ctx context.Context, caller Caller, req Request, groupID string, denial error, reason string) error {
	resource := "invalid-reference"
	if IsUUID(req.ObjectID) {
		resource = req.ObjectID
	}
	ev := AuditEvent{
		ActorID: caller.IdentityID, OrganizationID: caller.OrganizationID, GroupID: groupID,
		ResourceID: resource, Action: ActionSignGet, Outcome: "denied",
		TraceID: req.TraceID, RequestID: req.RequestID,
		Detail: map[string]any{"reason": reason},
	}
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return errors.Join(denial, fmt.Errorf("storage: denial audit: %w", err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := setTenant(ctx, tx, caller); err != nil {
		return errors.Join(denial, fmt.Errorf("storage: denial audit: %w", err))
	}
	if err := a.audit().Write(ctx, tx, ev); err != nil {
		return errors.Join(denial, fmt.Errorf("storage: denial audit: %w", err))
	}
	if err := tx.Commit(ctx); err != nil {
		return errors.Join(denial, fmt.Errorf("storage: denial audit: %w", err))
	}
	return denial
}

// setTenant applies transaction-local context (the fixture.SetTenant
// pattern): it resets at COMMIT or ROLLBACK and never leaks across pooled
// connections.
func setTenant(ctx context.Context, tx pgx.Tx, c Caller) error {
	_, err := tx.Exec(ctx,
		`SELECT set_config('app.organization_id', $1, true),
		        set_config('app.user_id', $2, true),
		        set_config('app.group_id', $3, true)`,
		c.OrganizationID, c.IdentityID, c.GroupID)
	return err
}

// MembershipReadCapability is the minimal storage.object.read check, read
// from rows visible to the caller under RLS:
//   - Group-scoped object: an Organization owner/admin, or a member of that
//     exact Group (group.read);
//   - Organization-scoped object: any Organization membership
//     (organization.read).
//
// A BusinessEntity grant alone confers no object read.
type MembershipReadCapability struct{}

func (MembershipReadCapability) CanReadObject(ctx context.Context, tx pgx.Tx, caller Caller, obj ObjectRecord) (bool, error) {
	if obj.OrganizationID != caller.OrganizationID {
		return false, nil
	}
	var ok bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM app.organization_memberships m
		               WHERE m.user_id = $1 AND m.organization_id = $2 AND m.role IN ('org_owner', 'org_admin'))
		    OR ($3::text = '' AND EXISTS (SELECT 1 FROM app.organization_memberships m
		                                  WHERE m.user_id = $1 AND m.organization_id = $2))
		    OR ($3::text <> '' AND EXISTS (SELECT 1 FROM app.group_memberships g
		                                   WHERE g.user_id = $1 AND g.organization_id = $2 AND g.group_id::text = $3::text))`,
		caller.IdentityID, caller.OrganizationID, obj.GroupID).Scan(&ok)
	return ok, err
}

// PGAudit writes audit events through the runtime role (INSERT only).
type PGAudit struct{}

func (PGAudit) Write(ctx context.Context, tx pgx.Tx, e AuditEvent) error {
	detail := e.Detail
	if detail == nil {
		detail = map[string]any{}
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO app.audit_events
		  (id, actor_type, actor_id, organization_id, group_id, resource_type, resource_id, action, outcome, trace_id, request_id, detail)
		VALUES (gen_random_uuid(), 'user', $1, NULLIF($2, '')::uuid, NULLIF($3, '')::uuid, 'stored_object', $4, $5, $6, $7, $8, $9)`,
		e.ActorID, e.OrganizationID, e.GroupID, e.ResourceID, e.Action, e.Outcome, e.TraceID, e.RequestID, detail)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("storage: audit insert affected %d rows", tag.RowsAffected())
	}
	return nil
}
