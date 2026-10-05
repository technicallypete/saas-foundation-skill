package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"example.invalid/archetype/isolation/policy"
)

// waitTimeout bounds every wait on a list_changed notification. Tests never
// sleep; they block on the notification channel up to this deadline.
const waitTimeout = 10 * time.Second

// fakeClock is a settable clock for expiry tests.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// harness is one in-memory client/server pair over a real SDK transport.
type harness struct {
	t         *testing.T
	store     *policy.MemoryStore
	audit     *MemoryAuditSink
	approvals *MemoryApprovals
	clock     *fakeClock
	sess      *Session
	cs        *sdk.ClientSession
	changed   chan struct{}
}

type env struct {
	store     *policy.MemoryStore
	audit     *MemoryAuditSink
	approvals *MemoryApprovals
	clock     *fakeClock
}

func newEnv() *env {
	return &env{
		store:     policy.NewSeededMemoryStore(),
		audit:     &MemoryAuditSink{},
		approvals: &MemoryApprovals{},
		clock:     &fakeClock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
	}
}

func (e *env) config() Config {
	return Config{Store: e.store, Audit: e.audit, Approvals: e.approvals, Now: e.clock.Now}
}

// connect authenticates identity (through the dev-only fixture
// authenticator below) and connects an SDK client over in-memory transports.
func (e *env) connect(t *testing.T, identity string) *harness {
	t.Helper()
	p, err := devAuthenticate(identity, e.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := NewSession(context.Background(), e.config(), p)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, store: e.store, audit: e.audit, approvals: e.approvals, clock: e.clock, sess: sess, changed: make(chan struct{}, 1)}
	st, ct := sdk.NewInMemoryTransports()
	ss, err := sess.Server().Connect(context.Background(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "fixture-client", Version: "0.0.0"}, &sdk.ClientOptions{
		ToolListChangedHandler: func(context.Context, *sdk.ToolListChangedRequest) {
			select {
			case h.changed <- struct{}{}:
			default: // a pending signal already covers this change
			}
		},
	})
	cs, err := client.Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.cs = cs
	t.Cleanup(func() {
		_ = cs.Close()
		_ = ss.Close()
	})
	return h
}

// devAuthenticate is the dev-only fixture authenticator. It exists only in
// test files and is never rendered into a generated project.
func devAuthenticate(identity string, now time.Time) (policy.Principal, error) {
	if identity == "" {
		return policy.Principal{}, errors.New("anonymous")
	}
	return policy.Principal{IdentityID: identity, ExpiresAt: now.Add(time.Hour)}, nil
}

func (h *harness) drain() {
	select {
	case <-h.changed:
	default:
	}
}

// waitChanged blocks until a tools/list_changed notification arrives.
func (h *harness) waitChanged() {
	h.t.Helper()
	select {
	case <-h.changed:
	case <-time.After(waitTimeout):
		h.t.Fatal("no tools/list_changed notification")
	}
}

func (h *harness) tools() []string {
	h.t.Helper()
	res, err := h.cs.ListTools(context.Background(), nil)
	if err != nil {
		h.t.Fatalf("tools/list: %v", err)
	}
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	slices.Sort(names)
	return names
}

func (h *harness) call(name string, args map[string]any) (*sdk.CallToolResult, error) {
	if args == nil {
		args = map[string]any{}
	}
	return h.cs.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
}

// ok calls a tool that must succeed and returns its structured content.
func (h *harness) ok(name string, args map[string]any) map[string]any {
	h.t.Helper()
	res, err := h.call(name, args)
	if err != nil {
		h.t.Fatalf("%s: protocol error %v", name, err)
	}
	if res.IsError {
		h.t.Fatalf("%s: tool error %s", name, text(res))
	}
	m, _ := res.StructuredContent.(map[string]any)
	return m
}

// switchOrg switches Organization and waits for the catalog notification.
func (h *harness) switchOrg(org string) {
	h.t.Helper()
	h.drain()
	h.ok("account.switch_organization", map[string]any{"organization": org})
	h.waitChanged()
}

func text(res *sdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// assertPolicyDenied asserts a handler policy denial: a successful
// protocol response carrying isError and error "policy_denied" — which the
// SDK's unknown-tool error (a JSON-RPC error) can never be.
func assertPolicyDenied(t *testing.T, res *sdk.CallToolResult, err error, reason string) {
	t.Helper()
	if err != nil {
		t.Fatalf("expected handler policy denial (%s), got protocol error %v", reason, err)
	}
	if !res.IsError {
		t.Fatalf("expected handler policy denial (%s), call succeeded: %s", reason, text(res))
	}
	m, _ := res.StructuredContent.(map[string]any)
	if m["error"] != ErrorPolicyDenied || m["reason"] != reason {
		t.Fatalf("expected policy_denied/%s, got %v", reason, res.StructuredContent)
	}
}

// assertUnknownTool asserts the SDK's unknown-tool protocol error.
func assertUnknownTool(t *testing.T, res *sdk.CallToolResult, err error, name string) *jsonrpc.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected unknown-tool protocol error, got result %v", name, res)
	}
	var we *jsonrpc.Error
	if !errors.As(err, &we) {
		t.Fatalf("%s: error %v (%T) is not a JSON-RPC error", name, err, err)
	}
	if we.Code != jsonrpc.CodeInvalidParams || !strings.Contains(we.Message, "unknown tool") {
		t.Fatalf("%s: unexpected protocol error %d %q", name, we.Code, we.Message)
	}
	return we
}

// lastEvent returns the most recent audit event for action.
func (h *harness) lastEvent(action string) (AuditEvent, bool) {
	evs := h.audit.Events()
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Action == action {
			return evs[i], true
		}
	}
	return AuditEvent{}, false
}

func (h *harness) assertDenialEvent(action, reason string) AuditEvent {
	h.t.Helper()
	ev, ok := h.lastEvent(action)
	if !ok || ev.Outcome != OutcomeDenied || ev.Reason != reason {
		h.t.Fatalf("no denial audit event for %s/%s (last: %+v)", action, reason, ev)
	}
	assertAttributed(h.t, ev)
	return ev
}

func assertAttributed(t *testing.T, ev AuditEvent) {
	t.Helper()
	if ev.OccurredAt.IsZero() || ev.ActorType == "" || ev.ActorID == "" || ev.ResourceType == "" ||
		ev.ResourceID == "" || ev.Action == "" || ev.Outcome == "" || ev.TraceID == "" || ev.RequestID == "" {
		t.Fatalf("audit event missing attribution: %+v", ev)
	}
}

func asJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
