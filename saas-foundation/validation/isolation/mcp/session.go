package mcp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"example.invalid/archetype/isolation/policy"
)

// Store is the authority and domain data a session reads and mutates. The
// in-memory policy.MemoryStore implements it.
type Store interface {
	policy.BindingStore
	policy.GroupDirectory
	// Version is the policy/version marker: it changes whenever bindings,
	// associations or Organization policy change.
	Version(ctx context.Context) (uint64, error)
	IdentityEmail(id string) (string, bool)
	Organization(id string) (policy.OrganizationInfo, bool)
	OrganizationIDBySlug(slug string) (string, bool)
	GroupIDBySlug(orgID, slug string) (string, bool)
	Group(id string) (policy.GroupInfo, bool)
	BusinessEntity(id string) (policy.BusinessEntityInfo, bool)
	RenameGroup(id, name string) error
	UpdateBusinessEntityDescription(id, description string) error
	TransferBusinessEntityOwnership(orgID, beID, newOwnerID string) error
	FindMembership(subjectID string, scope policy.Resource) (policy.Binding, bool)
	AddBinding(b policy.Binding) (string, error)
	RemoveBinding(id string) error
	SetBindingRole(id string, role policy.Role) error
}

// Config holds a session's dependencies.
type Config struct {
	Store     Store
	Audit     AuditSink
	Approvals Approvals
	Now       func() time.Time // nil means time.Now
	Logger    *slog.Logger     // nil discards SDK logs
}

// ErrUnauthenticated is returned when a session is created for a principal
// that is not authenticated.
var ErrUnauthenticated = errors.New("mcp: unauthenticated")

// Session is one authenticated session context with its own SDK Server.
// The SDK's tools/list returns every tool registered on a Server, so the
// registered set is the session's catalog and is reconciled from effective
// capabilities on context switches, on a policy/version marker change
// (checked on every request), on expiry and on logout.
type Session struct {
	cfg      Config
	eval     *policy.Evaluator
	resolver *policy.Resolver
	server   *sdk.Server
	traceID  string

	mu         sync.Mutex
	principal  policy.Principal
	tc         policy.TenantContext
	marker     uint64
	loggedOut  bool
	registered map[string]bool
	// suspended is a test hook: while set, catalog reconciliation (and the
	// context revalidation that accompanies it) is skipped, so stale tools
	// stay registered and handler reauthorization is observable in
	// isolation.
	suspended bool
}

// NewSession creates the session and its Server for an authenticated
// principal. The session starts without an Organization context.
func NewSession(ctx context.Context, cfg Config, p policy.Principal) (*Session, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.Store == nil || cfg.Audit == nil || cfg.Approvals == nil {
		return nil, errors.New("mcp: store, audit sink and approvals are required")
	}
	if !p.Valid(cfg.Now()) {
		return nil, ErrUnauthenticated
	}
	s := &Session{
		cfg:        cfg,
		eval:       policy.NewEvaluator(cfg.Store),
		resolver:   policy.NewResolver(cfg.Store, cfg.Now),
		traceID:    randomID(),
		principal:  p,
		registered: map[string]bool{},
	}
	s.server = sdk.NewServer(&sdk.Implementation{Name: "archetype-fixture", Version: "0.0.0"}, &sdk.ServerOptions{
		Logger:       cfg.Logger,
		Capabilities: &sdk.ServerCapabilities{Tools: &sdk.ToolCapabilities{ListChanged: true}},
	})
	s.server.AddReceivingMiddleware(s.refreshMiddleware)
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, err := cfg.Store.Version(ctx); err == nil {
		s.marker = v
	}
	s.applyCatalogLocked(ctx)
	return s, nil
}

// Server returns the session's SDK server.
func (s *Session) Server() *sdk.Server { return s.server }

// Context returns the current tenant context (zero when none).
func (s *Session) Context() policy.TenantContext {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tc
}

// LoggedOut reports whether the session was logged out.
func (s *Session) LoggedOut() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loggedOut
}

// SelectOrganization establishes Organization context through the trusted
// resolver (used for selectors such as a hostname). A failed selection
// leaves the session without context: a selector never grants anything.
func (s *Session) SelectOrganization(ctx context.Context, organizationID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tc, err := s.resolver.Resolve(ctx, s.principal, organizationID, "")
	if err != nil {
		return err
	}
	s.tc = tc
	s.applyCatalogLocked(ctx)
	return nil
}

// refreshMiddleware runs before every incoming request (tools/list,
// tools/call, ...), so the catalog follows the policy/version marker and
// session expiry per request.
func (s *Session) refreshMiddleware(next sdk.MethodHandler) sdk.MethodHandler {
	return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
		s.refresh(ctx)
		return next(ctx, method, req)
	}
}

// refresh revalidates the context when the marker changed and reconciles
// the catalog, unless reconciliation is suspended.
func (s *Session) refresh(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked(ctx)
}

func (s *Session) refreshLocked(ctx context.Context) {
	if s.suspended {
		return
	}
	v, err := s.cfg.Store.Version(ctx)
	if err != nil {
		// Fail closed: without a marker the catalog shrinks to nothing.
		s.tc = policy.TenantContext{}
		s.syncToolsLocked(nil)
		return
	}
	if v != s.marker {
		s.marker = v
		s.revalidateContextLocked(ctx)
	}
	s.applyCatalogLocked(ctx)
}

// revalidateContextLocked re-resolves the active context against current
// grants; a lost Group grant drops to Organization scope, a lost
// Organization grant drops all context.
func (s *Session) revalidateContextLocked(ctx context.Context) {
	if s.tc.OrganizationID == "" || s.loggedOut || !s.principal.Valid(s.cfg.Now()) {
		return
	}
	old := s.tc
	tc, err := s.resolver.Resolve(ctx, s.principal, old.OrganizationID, old.GroupID)
	if err != nil && old.GroupID != "" {
		tc, err = s.resolver.Resolve(ctx, s.principal, old.OrganizationID, "")
	}
	if err != nil {
		tc = policy.TenantContext{}
	}
	if tc != old {
		s.tc = tc
		s.auditLocked(AuditEvent{
			OrganizationID: old.OrganizationID, GroupID: old.GroupID,
			ResourceType: "session", ResourceID: s.traceID,
			Action: "context.invalidated", Outcome: OutcomeAllowed, Reason: "grants changed",
			RequestID: randomID(),
		})
	}
}

// desiredToolsLocked computes the catalog for the current state.
func (s *Session) desiredToolsLocked(ctx context.Context) []string {
	if s.loggedOut || !s.principal.Valid(s.cfg.Now()) {
		return nil
	}
	var names []string
	for _, d := range toolDefs {
		if d.scope == scopeAccount {
			names = append(names, d.name)
		}
	}
	if s.tc.OrganizationID == "" {
		return names
	}
	caps, err := s.eval.EffectiveCapabilities(ctx, s.tc)
	if err != nil {
		return names
	}
	for _, d := range toolDefs {
		switch {
		case d.scope == scopeContext:
			names = append(names, d.name)
		case d.scope == scopeCapability && caps.Has(d.capability):
			names = append(names, d.name)
		}
	}
	return names
}

func (s *Session) applyCatalogLocked(ctx context.Context) {
	s.syncToolsLocked(s.desiredToolsLocked(ctx))
}

// syncToolsLocked registers exactly the named tools. Unchanged tools are
// left alone so the SDK sends list_changed only for real changes.
func (s *Session) syncToolsLocked(names []string) {
	var remove []string
	for n := range s.registered {
		if !slices.Contains(names, n) {
			remove = append(remove, n)
		}
	}
	if len(remove) > 0 {
		slices.Sort(remove)
		s.server.RemoveTools(remove...)
		for _, n := range remove {
			delete(s.registered, n)
		}
	}
	for _, n := range names {
		if s.registered[n] {
			continue
		}
		d := toolByName[n]
		s.server.AddTool(d.tool(), s.handler(d))
		s.registered[n] = true
	}
}

// RegisteredTools returns the currently registered tool names, sorted.
func (s *Session) RegisteredTools() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for n := range s.registered {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

func (s *Session) auditLocked(e AuditEvent) {
	e.OccurredAt = s.cfg.Now().UTC()
	if e.ActorType == "" {
		e.ActorType = "user"
	}
	if e.ActorID == "" {
		e.ActorID = s.principal.IdentityID
	}
	e.TraceID = s.traceID
	if e.RequestID == "" {
		e.RequestID = randomID()
	}
	s.cfg.Audit.Record(e)
}
