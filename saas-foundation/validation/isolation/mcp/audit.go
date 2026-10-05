// Package mcp is the fixture MCP control plane built on the official MCP Go
// SDK. Each authenticated session context owns one SDK Server whose tool
// set is reconciled from effective capabilities; every tool handler
// independently reauthorizes. Tool names are illustrative (the MCP stage
// owns the real contract).
package mcp

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// Audit outcomes.
const (
	OutcomeAllowed = "allowed"
	OutcomeDenied  = "denied"
)

// AuditEvent carries the attribution fields of the audit foundation.
// OrganizationID may be empty only for events before any context exists.
type AuditEvent struct {
	OccurredAt     time.Time
	ActorType      string
	ActorID        string
	OrganizationID string
	GroupID        string
	ResourceType   string
	ResourceID     string
	Action         string
	Outcome        string
	Reason         string
	TraceID        string
	RequestID      string
}

// AuditSink receives audit events.
type AuditSink interface {
	Record(AuditEvent)
}

// MemoryAuditSink is an in-memory sink for tests.
type MemoryAuditSink struct {
	mu     sync.Mutex
	events []AuditEvent
}

// Record implements AuditSink.
func (m *MemoryAuditSink) Record(e AuditEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
}

// Events returns a copy of the recorded events.
func (m *MemoryAuditSink) Events() []AuditEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]AuditEvent(nil), m.events...)
}

// ApprovalRequest identifies one approval-required action exactly.
type ApprovalRequest struct {
	ApprovalID     string
	IdentityID     string
	OrganizationID string
	Action         string
	ResourceID     string
	Detail         string
}

// Approvals checks approvals for approval-required capabilities.
type Approvals interface {
	// Consume reports whether req carries a valid approval and, if so,
	// uses it up (approvals are single use).
	Consume(req ApprovalRequest) bool
}

// MemoryApprovals is an in-memory approval store. Approvals are granted by
// a trusted out-of-band path (never by the requesting tool call) and bound
// to the exact identity, Organization, action, resource and detail.
type MemoryApprovals struct {
	mu      sync.Mutex
	granted map[string]ApprovalRequest
}

// Grant records an approval and returns its ID.
func (m *MemoryApprovals) Grant(req ApprovalRequest) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.granted == nil {
		m.granted = map[string]ApprovalRequest{}
	}
	req.ApprovalID = randomID()
	m.granted[req.ApprovalID] = req
	return req.ApprovalID
}

// Consume implements Approvals.
func (m *MemoryApprovals) Consume(req ApprovalRequest) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.granted[req.ApprovalID]
	if !ok || req.ApprovalID == "" || g != req {
		return false
	}
	delete(m.granted, req.ApprovalID)
	return true
}

func randomID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
