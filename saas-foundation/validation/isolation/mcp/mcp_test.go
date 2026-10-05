package mcp

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"example.invalid/archetype/isolation/internal/fixture"
	"example.invalid/archetype/isolation/policy"
)

var (
	accountTools = []string{"account.get_current_context", "account.get_current_user", "account.list_organizations", "account.switch_organization", "logout"}
	contextTools = []string{"context.list_groups", "context.switch_group"}
)

func catalog(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	slices.Sort(out)
	return out
}

func assertTools(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("tools/list = %v\nwant        %v", got, want)
	}
}

func TestUnit_MCPToolNamesValid(t *testing.T) {
	fixture.Label(t, fixture.Unit)
	// The SDK only logs invalid names, so the fixture asserts them itself.
	pattern := regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	for _, n := range ToolNames() {
		if !pattern.MatchString(n) {
			t.Errorf("invalid tool name %q", n)
		}
	}
	want := []string{"account.get_current_user", "account.list_organizations", "account.get_current_context",
		"account.switch_organization", "context.list_groups", "context.switch_group", "logout"}
	if got := ControlPlaneToolNames(); !slices.Equal(got, want) {
		t.Errorf("control-plane tools = %v, want %v", got, want)
	}
	generic := []string{"groups.list", "groups.update", "business_entities.get", "business_entities.update",
		"business_entities.transfer_ownership", "members.manage"}
	for _, n := range generic {
		if !slices.Contains(ToolNames(), n) {
			t.Errorf("missing generic tool %s", n)
		}
	}
	if len(ToolNames()) != len(want)+len(generic) {
		t.Errorf("unexpected tool set %v", ToolNames())
	}
}

func TestUnit_MCPCatalogDiffersByOrganization(t *testing.T) {
	fixture.Label(t, fixture.RealSDKInMem)
	e := newEnv()
	h := e.connect(t, fixture.IdentityAda)
	assertTools(t, h.tools(), catalog(accountTools)) // no context yet

	h.switchOrg(fixture.OrgAlphaSlug)
	alpha := h.tools()
	assertTools(t, alpha, catalog(accountTools, contextTools, []string{
		"business_entities.get", "business_entities.transfer_ownership", "business_entities.update",
		"groups.list", "groups.update", "members.manage"}))

	h.switchOrg(fixture.OrgBravo)
	assertTools(t, h.tools(), catalog(accountTools, contextTools)) // org_member: nothing beyond control plane
	ctxBravo := h.ok("account.get_current_context", nil)
	if ctxBravo["organization_id"] != fixture.OrgBravo || asJSON(ctxBravo["capabilities"]) != `["organization.read"]` {
		t.Fatalf("bravo context = %v", ctxBravo)
	}

	// Other identities in alpha get their own catalogs.
	lin := e.connect(t, fixture.IdentityLin)
	lin.switchOrg(fixture.OrgAlpha)
	assertTools(t, lin.tools(), catalog(accountTools, contextTools, []string{
		"business_entities.get", "business_entities.update", "groups.list", "groups.update", "members.manage"}))
	kai := e.connect(t, fixture.IdentityKai)
	kai.switchOrg(fixture.OrgAlpha)
	assertTools(t, kai.tools(), catalog(accountTools, contextTools, []string{
		"business_entities.get", "business_entities.transfer_ownership", "business_entities.update"}))
}

func TestUnit_MCPOrganizationSwitchClearsGroup(t *testing.T) {
	fixture.Label(t, fixture.RealSDKInMem)
	e := newEnv()
	h := e.connect(t, fixture.IdentityAda)
	h.switchOrg(fixture.OrgAlpha)
	h.ok("context.switch_group", map[string]any{"group": "ga-one"})
	if got := h.ok("account.get_current_context", nil); got["group_id"] != fixture.GroupGA1 {
		t.Fatalf("group not selected: %v", got)
	}
	h.switchOrg(fixture.OrgBravo)
	if got := h.ok("account.get_current_context", nil); got["organization_id"] != fixture.OrgBravo || got["group_id"] != "" {
		t.Fatalf("Organization switch kept Group scope: %v", got)
	}
	h.switchOrg(fixture.OrgAlpha)
	if got := h.ok("account.get_current_context", nil); got["group_id"] != "" {
		t.Fatalf("Group scope restored after switching back: %v", got)
	}
	if ev, ok := h.lastEvent("context.switch_organization"); !ok || ev.Outcome != OutcomeAllowed || ev.OrganizationID != fixture.OrgAlpha {
		t.Fatalf("switch not audited: %+v", ev)
	}

	// A failed switch leaves the current context untouched.
	lin := e.connect(t, fixture.IdentityLin)
	lin.switchOrg(fixture.OrgAlpha)
	lin.ok("context.switch_group", map[string]any{"group": fixture.GroupGA1})
	res, err := lin.call("account.switch_organization", map[string]any{"organization": fixture.OrgBravoSlug})
	assertPolicyDenied(t, res, err, ReasonNotFound)
	if got := lin.ok("account.get_current_context", nil); got["organization_id"] != fixture.OrgAlpha || got["group_id"] != fixture.GroupGA1 {
		t.Fatalf("failed switch changed context: %v", got)
	}
	lin.assertDenialEvent("account.switch_organization", ReasonNotFound)
}

func TestUnit_MCPGroupSwitchRequiresGrant(t *testing.T) {
	fixture.Label(t, fixture.RealSDKInMem)
	e := newEnv()
	lin := e.connect(t, fixture.IdentityLin)
	lin.switchOrg(fixture.OrgAlpha)
	for _, sel := range []string{fixture.GroupGA2, "ga-two", fixture.GroupGB1, "gb-one", "f0000000-0000-4000-8000-00000000dead"} {
		res, err := lin.call("context.switch_group", map[string]any{"group": sel})
		assertPolicyDenied(t, res, err, ReasonNotFound)
	}
	lin.assertDenialEvent("context.switch_group", ReasonNotFound)
	if got := lin.ok("account.get_current_context", nil); got["group_id"] != "" {
		t.Fatalf("denied switch selected a Group: %v", got)
	}
	got := lin.ok("context.switch_group", map[string]any{"group": "ga-one"})
	if got["group_id"] != fixture.GroupGA1 {
		t.Fatalf("lin GA1 switch: %v", got)
	}

	// ada: org_owner in alpha may select any alpha Group; org_member in
	// bravo may select none.
	ada := e.connect(t, fixture.IdentityAda)
	ada.switchOrg(fixture.OrgAlpha)
	ada.ok("context.switch_group", map[string]any{"group": fixture.GroupGA2})
	ada.switchOrg(fixture.OrgBravo)
	res, err := ada.call("context.switch_group", map[string]any{"group": fixture.GroupGB1})
	assertPolicyDenied(t, res, err, ReasonNotFound)
}

func TestUnit_MCPListOrganizationsOnlyOwn(t *testing.T) {
	fixture.Label(t, fixture.RealSDKInMem)
	e := newEnv()
	ada := e.connect(t, fixture.IdentityAda).ok("account.list_organizations", nil)
	lin := e.connect(t, fixture.IdentityLin).ok("account.list_organizations", nil)
	sam := e.connect(t, fixture.IdentitySam).ok("account.list_organizations", nil)

	ids := func(m map[string]any) []string {
		var out []string
		for _, o := range m["organizations"].([]any) {
			out = append(out, o.(map[string]any)["organization_id"].(string))
		}
		slices.Sort(out)
		return out
	}
	if got := ids(ada); !slices.Equal(got, []string{fixture.OrgAlpha, fixture.OrgBravo}) {
		t.Errorf("ada organizations = %v", got)
	}
	if got := ids(lin); !slices.Equal(got, []string{fixture.OrgAlpha}) {
		t.Errorf("lin organizations = %v", got)
	}
	if !strings.Contains(asJSON(ada), `"organization_role":"org_owner"`) || !strings.Contains(asJSON(ada), `"organization_role":"org_member"`) {
		t.Errorf("ada roles missing: %s", asJSON(ada))
	}
	others := map[string]map[string]any{fixture.IdentityAda: ada, fixture.IdentityLin: lin, fixture.IdentitySam: sam}
	all := []string{fixture.IdentityAda, fixture.IdentityLin, fixture.IdentitySam, fixture.IdentityKai,
		"ada@example.test", "lin@example.test", "sam@example.test", "kai@example.test", "group_admin", "group_member", "be_owner"}
	for self, listing := range others {
		raw := asJSON(listing)
		for _, needle := range all {
			if needle != self && strings.Contains(raw, needle) {
				t.Errorf("listing for %s exposes %q: %s", self, needle, raw)
			}
		}
	}
}

func TestUnit_MCPHiddenToolFailsLikeUnknown(t *testing.T) {
	fixture.Label(t, fixture.RealSDKInMem)
	e := newEnv()
	h := e.connect(t, fixture.IdentityAda)
	h.switchOrg(fixture.OrgBravo) // org_member: transfer tool hidden
	before := len(e.audit.Events())

	hiddenName, unknownName := "business_entities.transfer_ownership", "no_such.tool"
	res, err := h.call(hiddenName, map[string]any{"business_entity_id": fixture.BusinessEntityB, "new_owner_id": fixture.IdentityAda})
	hidden := assertUnknownTool(t, res, err, hiddenName)
	res, err = h.call(unknownName, nil)
	unknown := assertUnknownTool(t, res, err, unknownName)
	if hidden.Code != unknown.Code ||
		strings.ReplaceAll(hidden.Message, hiddenName, "X") != strings.ReplaceAll(unknown.Message, unknownName, "X") ||
		string(hidden.Data) != string(unknown.Data) {
		t.Fatalf("hidden tool error %+v differs from unknown tool error %+v", hidden, unknown)
	}
	if len(e.audit.Events()) != before {
		t.Fatal("a hidden tool's handler ran")
	}
	// Without any context, capability tools are equally unknown.
	sam := e.connect(t, fixture.IdentitySam)
	res, err = sam.call("groups.list", nil)
	assertUnknownTool(t, res, err, "groups.list")
}

func TestUnit_MCPScopeArgumentsCannotChangeScope(t *testing.T) {
	fixture.Label(t, fixture.RealSDKInMem)
	e := newEnv()
	ada := e.connect(t, fixture.IdentityAda)
	ada.switchOrg(fixture.OrgAlpha)
	res, err := ada.call("groups.list", map[string]any{"organization_id": fixture.OrgBravo})
	assertPolicyDenied(t, res, err, ReasonScopeArgument)
	res, err = ada.call("business_entities.get", map[string]any{"business_entity_id": fixture.BusinessEntityB, "organization_id": fixture.OrgBravo})
	assertPolicyDenied(t, res, err, ReasonScopeArgument)
	ev := ada.assertDenialEvent("business_entities.get", ReasonScopeArgument)
	if ev.ResourceID != "organization_id" || ev.OrganizationID != fixture.OrgAlpha {
		t.Fatalf("scope-argument denial attribution: %+v", ev)
	}
	if got := ada.ok("account.get_current_context", nil); got["organization_id"] != fixture.OrgAlpha {
		t.Fatalf("scope argument changed context: %v", got)
	}
	// Without the scope argument, the foreign entity is simply not found.
	res, err = ada.call("business_entities.get", map[string]any{"business_entity_id": fixture.BusinessEntityB})
	assertPolicyDenied(t, res, err, ReasonNotFound)

	lin := e.connect(t, fixture.IdentityLin)
	lin.switchOrg(fixture.OrgAlpha)
	lin.ok("context.switch_group", map[string]any{"group": fixture.GroupGA1})
	res, err = lin.call("groups.update", map[string]any{"name": "Renamed", "group_id": fixture.GroupGA2})
	assertPolicyDenied(t, res, err, ReasonScopeArgument)
	if g, _ := e.store.Group(fixture.GroupGA2); g.Name != "Group A Two" {
		t.Fatalf("GA2 renamed through a scope argument: %q", g.Name)
	}
	// Control: the active Group is the only target.
	lin.ok("groups.update", map[string]any{"name": "Renamed"})
	if g, _ := e.store.Group(fixture.GroupGA1); g.Name != "Renamed" {
		t.Fatalf("control rename failed: %q", g.Name)
	}
}

func TestUnit_MCPApprovalRequired(t *testing.T) {
	fixture.Label(t, fixture.RealSDKInMem)
	e := newEnv()
	ada := e.connect(t, fixture.IdentityAda)
	ada.switchOrg(fixture.OrgAlpha)
	args := map[string]any{"business_entity_id": fixture.BusinessEntityA, "new_owner_id": fixture.IdentitySam}

	res, err := ada.call("business_entities.transfer_ownership", args)
	assertPolicyDenied(t, res, err, ReasonApprovalRequired)
	ada.assertDenialEvent("business_entities.transfer_ownership", ReasonApprovalRequired)

	// An approval for a different resource or new owner does not apply.
	wrong := e.approvals.Grant(ApprovalRequest{IdentityID: fixture.IdentityAda, OrganizationID: fixture.OrgAlpha,
		Action: "business_entities.transfer_ownership", ResourceID: fixture.BusinessEntityA2, Detail: fixture.IdentitySam})
	res, err = ada.call("business_entities.transfer_ownership", map[string]any{"business_entity_id": fixture.BusinessEntityA, "new_owner_id": fixture.IdentitySam, "approval_id": wrong})
	assertPolicyDenied(t, res, err, ReasonApprovalRequired)

	approval := e.approvals.Grant(ApprovalRequest{IdentityID: fixture.IdentityAda, OrganizationID: fixture.OrgAlpha,
		Action: "business_entities.transfer_ownership", ResourceID: fixture.BusinessEntityA, Detail: fixture.IdentitySam})
	withApproval := map[string]any{"business_entity_id": fixture.BusinessEntityA, "new_owner_id": fixture.IdentitySam, "approval_id": approval}
	ada.ok("business_entities.transfer_ownership", withApproval)
	if b, err := e.store.BindingsForSubject(t.Context(), fixture.IdentitySam, fixture.OrgAlpha); err != nil || len(b) != 2 {
		t.Fatalf("sam bindings after transfer: %+v", b)
	}
	// Approvals are single use.
	res, err = ada.call("business_entities.transfer_ownership", withApproval)
	assertPolicyDenied(t, res, err, ReasonApprovalRequired)

	// An approval never substitutes for the capability.
	lin := e.connect(t, fixture.IdentityLin)
	lin.switchOrg(fixture.OrgAlpha)
	res, err = lin.call("business_entities.transfer_ownership", args)
	assertUnknownTool(t, res, err, "business_entities.transfer_ownership") // hidden for group_admin
}

func TestUnit_MCPLogoutInvalidates(t *testing.T) {
	fixture.Label(t, fixture.RealSDKInMem)
	e := newEnv()
	h := e.connect(t, fixture.IdentityAda)
	h.switchOrg(fixture.OrgAlpha)
	h.drain()
	h.ok("logout", nil)
	h.waitChanged()
	assertTools(t, h.tools(), nil)
	if !h.sess.LoggedOut() {
		t.Fatal("session not marked logged out")
	}
	for _, n := range []string{"account.get_current_user", "groups.list", "logout"} {
		res, err := h.call(n, nil)
		assertUnknownTool(t, res, err, n)
	}
	if ev, ok := h.lastEvent("session.logout"); !ok || ev.Outcome != OutcomeAllowed {
		t.Fatalf("logout not audited: %+v", ev)
	}
}

func TestUnit_MCPAuditEvents(t *testing.T) {
	fixture.Label(t, fixture.RealSDKInMem)
	e := newEnv()
	lin := e.connect(t, fixture.IdentityLin)
	lin.switchOrg(fixture.OrgAlpha)
	lin.ok("context.switch_group", map[string]any{"group": fixture.GroupGA1})
	lin.ok("groups.update", map[string]any{"name": "Group A One Renamed"})
	lin.ok("members.manage", map[string]any{"subject_id": fixture.IdentitySam, "role": "group_member", "action": "grant"})
	res, err := lin.call("members.manage", map[string]any{"subject_id": fixture.IdentitySam, "role": "org_admin", "action": "grant"})
	assertPolicyDenied(t, res, err, ReasonForbidden)

	want := map[string]string{
		"context.switch_organization": OutcomeAllowed,
		"context.switch_group":        OutcomeAllowed,
		"group.update":                OutcomeAllowed,
		"membership.role_change":      OutcomeAllowed,
		"members.manage":              OutcomeDenied,
	}
	for action, outcome := range want {
		ev, ok := lin.lastEvent(action)
		if !ok || ev.Outcome != outcome {
			t.Errorf("audit %s: %+v", action, ev)
			continue
		}
		assertAttributed(t, ev)
		if ev.ActorID != fixture.IdentityLin || ev.OrganizationID != fixture.OrgAlpha {
			t.Errorf("audit %s attribution: %+v", action, ev)
		}
	}
	if ev, _ := lin.lastEvent("membership.role_change"); ev.GroupID != fixture.GroupGA1 || ev.ResourceID != fixture.GroupGA1 {
		t.Errorf("role change attribution: %+v", ev)
	}
	// Request IDs are per call; the trace ID is per session.
	evs := e.audit.Events()
	seen := map[string]bool{}
	for _, ev := range evs {
		if seen[ev.RequestID] {
			t.Errorf("request ID reused: %+v", ev)
		}
		seen[ev.RequestID] = true
		if ev.TraceID != evs[0].TraceID {
			t.Errorf("trace ID changed within one session")
		}
	}
	if _, ok := e.store.FindMembership(fixture.IdentitySam, policy.Resource{Type: policy.ScopeGroup, ID: fixture.GroupGA1}); !ok {
		t.Error("role grant not applied")
	}
}

// --- Reauthorization with catalog reconciliation suspended (H6) ---

func (h *harness) suspend(on bool) {
	h.sess.mu.Lock()
	defer h.sess.mu.Unlock()
	h.sess.suspended = on
}

// staleCallDenied asserts that tool is still registered (reconciliation is
// suspended), that calling it yields a handler policy denial with reason,
// that the denial is audited, and that it is distinguishable from the SDK
// unknown-tool error. Resuming reconciliation then removes the tool.
func (h *harness) staleCallDenied(tool string, args map[string]any, reason string, stillListed bool) {
	t := h.t
	t.Helper()
	if !slices.Contains(h.tools(), tool) {
		t.Fatalf("%s left the catalog while reconciliation was suspended", tool)
	}
	res, err := h.call(tool, args)
	assertPolicyDenied(t, res, err, reason)
	h.assertDenialEvent(tool, reason)
	res, err = h.call("no_such.tool", nil)
	assertUnknownTool(t, res, err, "no_such.tool")

	h.suspend(false)
	h.drain()
	listed := h.tools() // per-request marker check reconciles now
	if slices.Contains(listed, tool) != stillListed {
		t.Fatalf("after reconciliation %s listed=%t, want %t (%v)", tool, !stillListed, stillListed, listed)
	}
	if !stillListed {
		h.waitChanged()
		res, err = h.call(tool, args)
		assertUnknownTool(t, res, err, tool)
	}
}

func TestUnit_MCPSuspendedCatalogRevokedGrant(t *testing.T) {
	fixture.Label(t, fixture.RealSDKInMem)
	e := newEnv()
	// kai additionally reads BE-A through GA2, so revoking the direct grant
	// leaves a valid context and the denial is purely the capability.
	if _, err := e.store.AddBinding(policy.Binding{OrganizationID: fixture.OrgAlpha, SubjectID: fixture.IdentityKai,
		ScopeType: policy.ScopeGroup, ScopeID: fixture.GroupGA2, Role: policy.RoleGroupMember, Source: policy.SourceMembership}); err != nil {
		t.Fatal(err)
	}
	kai := e.connect(t, fixture.IdentityKai)
	kai.switchOrg(fixture.OrgAlpha)
	upd := map[string]any{"business_entity_id": fixture.BusinessEntityA, "description": "updated"}
	kai.ok("business_entities.update", upd) // positive control
	kai.suspend(true)
	if err := e.store.RemoveBinding(policy.SeedBindingKaiBEA); err != nil {
		t.Fatal(err)
	}
	kai.staleCallDenied("business_entities.update", upd, ReasonForbidden, false)

	// Revoking an identity's only grant invalidates the context itself.
	sam := e.connect(t, fixture.IdentitySam)
	sam.switchOrg(fixture.OrgAlpha)
	get := map[string]any{"business_entity_id": fixture.BusinessEntityA2}
	sam.ok("business_entities.get", get)
	sam.suspend(true)
	if err := e.store.RemoveBinding(policy.SeedBindingSamGA2); err != nil {
		t.Fatal(err)
	}
	sam.staleCallDenied("business_entities.get", get, ReasonContextInvalid, false)
	if got := sam.sess.Context(); got.OrganizationID != "" {
		t.Fatalf("revoked context survived reconciliation: %+v", got)
	}
}

func TestUnit_MCPSuspendedCatalogRoleDowngrade(t *testing.T) {
	fixture.Label(t, fixture.RealSDKInMem)
	e := newEnv()
	ada := e.connect(t, fixture.IdentityAda)
	ada.switchOrg(fixture.OrgAlpha)
	// Positive control: an approved transfer of BE-A2 succeeds.
	a1 := e.approvals.Grant(ApprovalRequest{IdentityID: fixture.IdentityAda, OrganizationID: fixture.OrgAlpha,
		Action: "business_entities.transfer_ownership", ResourceID: fixture.BusinessEntityA2, Detail: fixture.IdentityKai})
	ada.ok("business_entities.transfer_ownership", map[string]any{"business_entity_id": fixture.BusinessEntityA2, "new_owner_id": fixture.IdentityKai, "approval_id": a1})

	ada.suspend(true)
	if err := e.store.SetBindingRole(policy.SeedBindingAdaAlpha, policy.RoleOrgAdmin); err != nil {
		t.Fatal(err)
	}
	// A valid approval is presented, so the denial is the downgraded role.
	a2 := e.approvals.Grant(ApprovalRequest{IdentityID: fixture.IdentityAda, OrganizationID: fixture.OrgAlpha,
		Action: "business_entities.transfer_ownership", ResourceID: fixture.BusinessEntityA, Detail: fixture.IdentitySam})
	ada.staleCallDenied("business_entities.transfer_ownership",
		map[string]any{"business_entity_id": fixture.BusinessEntityA, "new_owner_id": fixture.IdentitySam, "approval_id": a2},
		ReasonForbidden, false)
	if !slices.Contains(ada.tools(), "groups.update") {
		t.Fatal("org_admin lost group.update")
	}
}

func TestUnit_MCPSuspendedCatalogPolicyToggle(t *testing.T) {
	fixture.Label(t, fixture.RealSDKInMem)
	e := newEnv()
	lin := e.connect(t, fixture.IdentityLin)
	lin.switchOrg(fixture.OrgAlpha)
	upd := map[string]any{"business_entity_id": fixture.BusinessEntityA, "description": "by group admin"}
	lin.ok("business_entities.update", upd)
	lin.suspend(true)
	e.store.SetOrganizationPolicy(fixture.OrgAlpha, policy.OrganizationPolicy{GroupAdminsEditBusinessProfiles: false})
	lin.staleCallDenied("business_entities.update", upd, ReasonForbidden, false)
	lin.ok("business_entities.get", map[string]any{"business_entity_id": fixture.BusinessEntityA}) // read stays
}

func TestUnit_MCPSuspendedCatalogSessionExpired(t *testing.T) {
	fixture.Label(t, fixture.RealSDKInMem)
	e := newEnv()
	ada := e.connect(t, fixture.IdentityAda)
	ada.switchOrg(fixture.OrgAlpha)
	ada.ok("groups.list", nil)
	ada.suspend(true)
	e.clock.Advance(2 * time.Hour)
	res, err := ada.call("account.get_current_user", nil)
	assertPolicyDenied(t, res, err, ReasonSessionExpired)
	ada.staleCallDenied("groups.list", nil, ReasonSessionExpired, false)
	assertTools(t, ada.tools(), nil) // expiry empties the catalog
}
