package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"example.invalid/archetype/isolation/policy"
)

// Error codes in a denied or failed tool result's structured content. A
// policy denial is a tool result (isError with error "policy_denied"),
// never a protocol error, so it is distinguishable from the SDK's
// unknown-tool error for tools absent from the catalog.
const (
	ErrorPolicyDenied    = "policy_denied"
	ErrorInvalidArgument = "invalid_arguments"
)

// Denial reasons.
const (
	ReasonUnauthenticated     = "unauthenticated"
	ReasonSessionExpired      = "session_expired"
	ReasonScopeArgument       = "scope_argument_rejected"
	ReasonNoOrganization      = "no_active_organization"
	ReasonContextInvalid      = "context_invalid"
	ReasonGroupRequired       = "group_context_required"
	ReasonNotFound            = "not_found"
	ReasonForbidden           = "forbidden"
	ReasonApprovalRequired    = "approval_required"
	ReasonInvalidRoleOrTarget = "invalid_role_or_target"
)

// scopeArguments can never change scope; tool calls carrying them are
// rejected outright. Context changes only through the switch tools.
var scopeArguments = []string{"organization_id", "group_id"}

type toolScope int

const (
	scopeAccount    toolScope = iota // available whenever authenticated
	scopeContext                     // needs an active Organization
	scopeCapability                  // needs an active Organization and a capability
)

// denial is a policy decision returned by a tool implementation.
type denial struct {
	reason       string
	resourceType string
	resourceID   string
}

func (d *denial) Error() string { return "policy denied: " + d.reason }

func deny(reason, resourceType, resourceID string) *denial {
	return &denial{reason: reason, resourceType: resourceType, resourceID: resourceID}
}

// fromPolicy converts a policy error into a denial (or passes through a
// store failure).
func fromPolicy(err error, r policy.Resource) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, policy.ErrNotFound):
		return deny(ReasonNotFound, string(r.Type), r.ID)
	case errors.Is(err, policy.ErrForbidden):
		return deny(ReasonForbidden, string(r.Type), r.ID)
	case errors.Is(err, policy.ErrUnauthenticated):
		return deny(ReasonUnauthenticated, string(r.Type), r.ID)
	}
	return err
}

// call is one reauthorized tool invocation.
type call struct {
	s         *Session
	tool      string
	requestID string
	principal policy.Principal
	tc        policy.TenantContext // revalidated for context/capability tools
	args      map[string]json.RawMessage
}

func (c *call) str(name string, required bool) (string, error) {
	raw, ok := c.args[name]
	if !ok {
		if required {
			return "", fmt.Errorf("missing argument %q", name)
		}
		return "", nil
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("argument %q must be a string", name)
	}
	if required && v == "" {
		return "", fmt.Errorf("argument %q must not be empty", name)
	}
	return v, nil
}

// audit records an event attributed to this call.
func (c *call) audit(action, outcome, reason, resourceType, resourceID string) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	c.s.auditLocked(AuditEvent{
		ActorID: c.principal.IdentityID, OrganizationID: c.tc.OrganizationID, GroupID: c.tc.GroupID,
		ResourceType: resourceType, ResourceID: resourceID,
		Action: action, Outcome: outcome, Reason: reason, RequestID: c.requestID,
	})
}

type toolDef struct {
	name        string
	description string
	scope       toolScope
	capability  policy.Capability
	props       map[string]string // argument -> description (all strings)
	required    []string
	run         func(ctx context.Context, c *call) (any, error)
}

func (d toolDef) tool() *sdk.Tool {
	props := map[string]any{}
	for k, desc := range d.props {
		props[k] = map[string]any{"type": "string", "description": desc}
	}
	schema := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(d.required) > 0 {
		schema["required"] = d.required
	}
	return &sdk.Tool{Name: d.name, Description: d.description, InputSchema: schema}
}

// Tool definitions. Control-plane names follow the canonical "MCP context
// switching terminology"; capability tools are generic fixtures.
// toolDefs and toolByName are built in init: the tool implementations refer
// back to the session (and so to the catalog), which a package-level
// initializer would turn into an initialization cycle.
var (
	toolDefs   []toolDef
	toolByName map[string]toolDef
)

func init() {
	toolDefs = []toolDef{
		{name: "account.get_current_user", description: "Return the authenticated identity.", scope: scopeAccount, run: getCurrentUser},
		{name: "account.list_organizations", description: "List the caller's own Organizations.", scope: scopeAccount, run: listOrganizations},
		{name: "account.get_current_context", description: "Return the active Organization and Group.", scope: scopeAccount, run: getCurrentContext},
		{name: "account.switch_organization", description: "Switch the active Organization (clears Group scope).", scope: scopeAccount,
			props: map[string]string{"organization": "Organization ID or slug"}, required: []string{"organization"}, run: switchOrganization},
		{name: "context.list_groups", description: "List Groups selectable in the active Organization.", scope: scopeContext, run: listGroups},
		{name: "context.switch_group", description: "Switch the active Group within the active Organization.", scope: scopeContext,
			props: map[string]string{"group": "Group ID or slug"}, required: []string{"group"}, run: switchGroup},
		{name: "logout", description: "End this session.", scope: scopeAccount, run: logout},

		{name: "groups.list", description: "List readable Groups.", scope: scopeCapability, capability: policy.CapGroupRead, run: listGroups},
		{name: "groups.update", description: "Rename the active Group.", scope: scopeCapability, capability: policy.CapGroupUpdate,
			props: map[string]string{"name": "New display name"}, required: []string{"name"}, run: updateGroup},
		{name: "business_entities.get", description: "Read a BusinessEntity profile.", scope: scopeCapability, capability: policy.CapBusinessEntityProfileRead,
			props: map[string]string{"business_entity_id": "BusinessEntity ID"}, required: []string{"business_entity_id"}, run: getBusinessEntity},
		{name: "business_entities.update", description: "Update a BusinessEntity profile.", scope: scopeCapability, capability: policy.CapBusinessEntityProfileUpdate,
			props: map[string]string{"business_entity_id": "BusinessEntity ID", "description": "Profile description"}, required: []string{"business_entity_id", "description"}, run: updateBusinessEntity},
		{name: "business_entities.transfer_ownership", description: "Transfer BusinessEntity ownership (approval required).", scope: scopeCapability, capability: policy.CapBusinessEntityOwnershipTransfer,
			props:    map[string]string{"business_entity_id": "BusinessEntity ID", "new_owner_id": "Identity ID of the new owner", "approval_id": "Approval issued out of band"},
			required: []string{"business_entity_id", "new_owner_id"}, run: transferOwnership},
		{name: "members.manage", description: "Grant, change or revoke a role in the active Group.", scope: scopeCapability, capability: policy.CapGroupManageMembers,
			props:    map[string]string{"subject_id": "Identity ID", "role": "group_admin or group_member", "action": "grant or revoke"},
			required: []string{"subject_id", "action"}, run: manageMembers},
	}
	toolByName = map[string]toolDef{}
	for _, d := range toolDefs {
		toolByName[d.name] = d
	}
}

// ToolNames returns every tool name the fixture can register.
func ToolNames() []string {
	var out []string
	for _, d := range toolDefs {
		out = append(out, d.name)
	}
	return out
}

// ControlPlaneToolNames returns the control-plane tool names.
func ControlPlaneToolNames() []string {
	var out []string
	for _, d := range toolDefs {
		if d.scope != scopeCapability {
			out = append(out, d.name)
		}
	}
	return out
}

func result(structured any, isError bool) *sdk.CallToolResult {
	text, _ := json.Marshal(structured)
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: string(text)}}, StructuredContent: structured, IsError: isError}
}

// handler wraps a tool implementation with independent reauthorization:
// authenticated and unexpired principal, no scope-changing arguments, the
// active Organization/Group revalidated against current bindings, then
// the implementation's own resource/capability/approval checks.
func (s *Session) handler(d toolDef) sdk.ToolHandler {
	return func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		s.mu.Lock()
		c := &call{s: s, tool: d.name, requestID: randomID(), principal: s.principal, tc: s.tc}
		loggedOut := s.loggedOut
		now := s.cfg.Now()
		s.mu.Unlock()

		out, err := s.authorizeAndRun(ctx, c, d, req, loggedOut, now)
		var den *denial
		switch {
		case errors.As(err, &den):
			rt, rid := den.resourceType, den.resourceID
			if rt == "" {
				rt, rid = "tool", d.name
			}
			c.audit(d.name, OutcomeDenied, den.reason, rt, rid)
			return result(map[string]any{"error": ErrorPolicyDenied, "reason": den.reason}, true), nil
		case errors.Is(err, errInvalidArgs):
			return result(map[string]any{"error": ErrorInvalidArgument, "message": err.Error()}, true), nil
		case err != nil:
			return nil, err
		}
		return result(out, false), nil
	}
}

var errInvalidArgs = errors.New("invalid arguments")

func (s *Session) authorizeAndRun(ctx context.Context, c *call, d toolDef, req *sdk.CallToolRequest, loggedOut bool, now time.Time) (any, error) {
	if loggedOut {
		return nil, deny(ReasonUnauthenticated, "", "")
	}
	if !c.principal.Valid(now) {
		return nil, deny(ReasonSessionExpired, "", "")
	}
	c.args = map[string]json.RawMessage{}
	if req.Params != nil && len(req.Params.Arguments) > 0 && string(req.Params.Arguments) != "null" {
		if err := json.Unmarshal(req.Params.Arguments, &c.args); err != nil {
			return nil, fmt.Errorf("%w: arguments must be an object", errInvalidArgs)
		}
	}
	for _, a := range scopeArguments {
		if _, ok := c.args[a]; ok {
			return nil, deny(ReasonScopeArgument, "argument", a)
		}
	}
	for a := range c.args {
		if _, ok := d.props[a]; !ok {
			return nil, fmt.Errorf("%w: unknown argument %q", errInvalidArgs, a)
		}
	}
	if d.scope != scopeAccount {
		if c.tc.OrganizationID == "" {
			return nil, deny(ReasonNoOrganization, "", "")
		}
		tc, err := s.resolver.Resolve(ctx, c.principal, c.tc.OrganizationID, c.tc.GroupID)
		if err != nil {
			if policy.IsDenial(err) {
				return nil, deny(ReasonContextInvalid, "organization", c.tc.OrganizationID)
			}
			return nil, err
		}
		c.tc = tc
	}
	out, err := d.run(ctx, c)
	var ae argError
	if errors.As(err, &ae) {
		return nil, fmt.Errorf("%w: %s", errInvalidArgs, ae.msg)
	}
	return out, err
}

type argError struct{ msg string }

func (e argError) Error() string { return e.msg }

func args(c *call, names ...string) ([]string, error) {
	d := toolByName[c.tool]
	out := make([]string, len(names))
	for i, n := range names {
		v, err := c.str(n, slices.Contains(d.required, n))
		if err != nil {
			return nil, argError{err.Error()}
		}
		out[i] = v
	}
	return out, nil
}

func getCurrentUser(_ context.Context, c *call) (any, error) {
	email, _ := c.s.cfg.Store.IdentityEmail(c.principal.IdentityID)
	return map[string]any{"identity_id": c.principal.IdentityID, "email": email}, nil
}

// listOrganizations returns only the caller's own Organizations and the
// caller's own Organization-level role; no other identity's memberships.
func listOrganizations(ctx context.Context, c *call) (any, error) {
	ids, err := c.s.cfg.Store.OrganizationsForSubject(ctx, c.principal.IdentityID)
	if err != nil {
		return nil, err
	}
	orgs := []map[string]any{}
	for _, id := range ids {
		info, _ := c.s.cfg.Store.Organization(id)
		role := ""
		if b, ok := c.s.cfg.Store.FindMembership(c.principal.IdentityID, policy.Resource{Type: policy.ScopeOrganization, ID: id}); ok {
			role = string(b.Role)
		}
		orgs = append(orgs, map[string]any{"organization_id": id, "slug": info.Slug, "name": info.Name, "organization_role": role})
	}
	return map[string]any{"organizations": orgs}, nil
}

func getCurrentContext(ctx context.Context, c *call) (any, error) {
	caps := []string{}
	if c.tc.OrganizationID != "" {
		set, err := c.s.eval.EffectiveCapabilities(ctx, c.tc)
		if err != nil {
			return nil, err
		}
		for _, x := range set.Sorted() {
			caps = append(caps, string(x))
		}
	}
	return map[string]any{"organization_id": c.tc.OrganizationID, "group_id": c.tc.GroupID, "capabilities": caps}, nil
}

func switchOrganization(ctx context.Context, c *call) (any, error) {
	a, err := args(c, "organization")
	if err != nil {
		return nil, err
	}
	target := a[0]
	if id, ok := c.s.cfg.Store.OrganizationIDBySlug(target); ok {
		target = id
	}
	tc, err := c.s.resolver.Resolve(ctx, c.principal, target, "")
	if err != nil {
		if policy.IsDenial(err) {
			return nil, deny(ReasonNotFound, "organization", a[0])
		}
		return nil, err
	}
	c.s.mu.Lock()
	c.s.tc = tc // Group scope is cleared on every Organization switch
	c.s.applyCatalogLocked(ctx)
	c.s.mu.Unlock()
	c.tc = tc
	c.audit("context.switch_organization", OutcomeAllowed, "", "organization", tc.OrganizationID)
	return map[string]any{"organization_id": tc.OrganizationID, "group_id": ""}, nil
}

func switchGroup(ctx context.Context, c *call) (any, error) {
	a, err := args(c, "group")
	if err != nil {
		return nil, err
	}
	target := a[0]
	if id, ok := c.s.cfg.Store.GroupIDBySlug(c.tc.OrganizationID, target); ok {
		target = id
	}
	tc, err := c.s.resolver.Resolve(ctx, c.principal, c.tc.OrganizationID, target)
	if err != nil {
		if policy.IsDenial(err) {
			return nil, deny(ReasonNotFound, "group", a[0])
		}
		return nil, err
	}
	c.s.mu.Lock()
	c.s.tc = tc
	c.s.applyCatalogLocked(ctx)
	c.s.mu.Unlock()
	c.tc = tc
	c.audit("context.switch_group", OutcomeAllowed, "", "group", tc.GroupID)
	return map[string]any{"organization_id": tc.OrganizationID, "group_id": tc.GroupID}, nil
}

func logout(ctx context.Context, c *call) (any, error) {
	c.s.mu.Lock()
	c.s.loggedOut = true
	c.s.applyCatalogLocked(ctx) // empty catalog
	c.s.mu.Unlock()
	c.audit("session.logout", OutcomeAllowed, "", "session", c.s.traceID)
	return map[string]any{"logged_out": true}, nil
}

func listGroups(ctx context.Context, c *call) (any, error) {
	gs, err := policy.VisibleGroups(ctx, c.s.eval, c.s.cfg.Store, c.tc)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, g := range gs {
		out = append(out, map[string]any{"group_id": g.ID, "slug": g.Slug, "name": g.Name})
	}
	return map[string]any{"groups": out}, nil
}

func updateGroup(ctx context.Context, c *call) (any, error) {
	a, err := args(c, "name")
	if err != nil {
		return nil, err
	}
	if c.tc.GroupID == "" {
		return nil, deny(ReasonGroupRequired, "", "")
	}
	r := policy.Resource{Type: policy.ScopeGroup, ID: c.tc.GroupID}
	if err := fromPolicy(c.s.eval.Authorize(ctx, c.tc, policy.CapGroupUpdate, r), r); err != nil {
		return nil, err
	}
	if err := c.s.cfg.Store.RenameGroup(c.tc.GroupID, a[0]); err != nil {
		return nil, err
	}
	c.audit("group.update", OutcomeAllowed, "", "group", c.tc.GroupID)
	return map[string]any{"group_id": c.tc.GroupID, "name": a[0]}, nil
}

func getBusinessEntity(ctx context.Context, c *call) (any, error) {
	a, err := args(c, "business_entity_id")
	if err != nil {
		return nil, err
	}
	r := policy.Resource{Type: policy.ScopeBusinessEntity, ID: a[0]}
	if err := fromPolicy(c.s.eval.Authorize(ctx, c.tc, policy.CapBusinessEntityProfileRead, r), r); err != nil {
		return nil, err
	}
	e, _ := c.s.cfg.Store.BusinessEntity(a[0])
	return map[string]any{"business_entity_id": e.ID, "name": e.Name, "description": e.Description}, nil
}

func updateBusinessEntity(ctx context.Context, c *call) (any, error) {
	a, err := args(c, "business_entity_id", "description")
	if err != nil {
		return nil, err
	}
	r := policy.Resource{Type: policy.ScopeBusinessEntity, ID: a[0]}
	if err := fromPolicy(c.s.eval.Authorize(ctx, c.tc, policy.CapBusinessEntityProfileUpdate, r), r); err != nil {
		return nil, err
	}
	if err := c.s.cfg.Store.UpdateBusinessEntityDescription(a[0], a[1]); err != nil {
		return nil, err
	}
	c.audit("business_entity.profile.update", OutcomeAllowed, "", "business_entity", a[0])
	return map[string]any{"business_entity_id": a[0], "description": a[1]}, nil
}

func transferOwnership(ctx context.Context, c *call) (any, error) {
	a, err := args(c, "business_entity_id", "new_owner_id", "approval_id")
	if err != nil {
		return nil, err
	}
	r := policy.Resource{Type: policy.ScopeBusinessEntity, ID: a[0]}
	if err := fromPolicy(c.s.eval.Authorize(ctx, c.tc, policy.CapBusinessEntityOwnershipTransfer, r), r); err != nil {
		return nil, err
	}
	// The new owner must already belong to the active Organization.
	orgs, err := c.s.cfg.Store.OrganizationsForSubject(ctx, a[1])
	if err != nil {
		return nil, err
	}
	if !slices.Contains(orgs, c.tc.OrganizationID) {
		return nil, deny(ReasonInvalidRoleOrTarget, "identity", a[1])
	}
	approved := c.s.cfg.Approvals.Consume(ApprovalRequest{
		ApprovalID: a[2], IdentityID: c.principal.IdentityID, OrganizationID: c.tc.OrganizationID,
		Action: "business_entities.transfer_ownership", ResourceID: a[0], Detail: a[1],
	})
	if !approved {
		return nil, deny(ReasonApprovalRequired, "business_entity", a[0])
	}
	if err := c.s.cfg.Store.TransferBusinessEntityOwnership(c.tc.OrganizationID, a[0], a[1]); err != nil {
		return nil, err
	}
	c.audit("membership.role_change", OutcomeAllowed, "be_owner transferred", "business_entity", a[0])
	c.s.refresh(ctx)
	return map[string]any{"business_entity_id": a[0], "owner_id": a[1]}, nil
}

func manageMembers(ctx context.Context, c *call) (any, error) {
	a, err := args(c, "subject_id", "role", "action")
	if err != nil {
		return nil, err
	}
	subject, role, action := a[0], policy.Role(a[1]), a[2]
	if c.tc.GroupID == "" {
		return nil, deny(ReasonGroupRequired, "", "")
	}
	scope := policy.Resource{Type: policy.ScopeGroup, ID: c.tc.GroupID}
	// The subject must exist AND already belong to the active Organization;
	// both failures return the same denial, so the tool is not an oracle for
	// identities elsewhere and cannot pull outsiders into the Organization.
	if _, ok := c.s.cfg.Store.IdentityEmail(subject); !ok {
		return nil, deny(ReasonInvalidRoleOrTarget, "identity", subject)
	}
	orgs, err := c.s.cfg.Store.OrganizationsForSubject(ctx, subject)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(orgs, c.tc.OrganizationID) {
		return nil, deny(ReasonInvalidRoleOrTarget, "identity", subject)
	}
	existing, has := c.s.cfg.Store.FindMembership(subject, scope)
	switch action {
	case "grant":
		if err := fromPolicy(c.s.eval.AuthorizeRoleGrant(ctx, c.tc, scope, role), scope); err != nil {
			return nil, err
		}
		if has {
			// Changing a role is also authorized against the role being replaced.
			if err := fromPolicy(c.s.eval.AuthorizeRoleGrant(ctx, c.tc, scope, existing.Role), scope); err != nil {
				return nil, err
			}
		}
		if has {
			err = c.s.cfg.Store.SetBindingRole(existing.ID, role)
		} else {
			_, err = c.s.cfg.Store.AddBinding(policy.Binding{
				OrganizationID: c.tc.OrganizationID, SubjectID: subject, ScopeType: policy.ScopeGroup,
				ScopeID: c.tc.GroupID, Role: role, Source: policy.SourceMembership,
			})
		}
	case "revoke":
		if !has {
			return nil, deny(ReasonInvalidRoleOrTarget, "identity", subject)
		}
		if err := fromPolicy(c.s.eval.AuthorizeRoleGrant(ctx, c.tc, scope, existing.Role), scope); err != nil {
			return nil, err
		}
		err = c.s.cfg.Store.RemoveBinding(existing.ID)
	default:
		return nil, argError{fmt.Sprintf("action must be grant or revoke, got %q", action)}
	}
	if err != nil {
		return nil, err
	}
	c.audit("membership.role_change", OutcomeAllowed, action+" "+string(role)+" for "+subject, "group", c.tc.GroupID)
	c.s.refresh(ctx) // the caller's own catalog may change
	return map[string]any{"group_id": c.tc.GroupID, "subject_id": subject, "action": action}, nil
}
