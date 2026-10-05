package policy_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"example.invalid/archetype/isolation/internal/fixture"
	"example.invalid/archetype/isolation/policy"
)

const unknownID = "f0000000-0000-4000-8000-00000000dead"

var ctx = context.Background()

func valid(id string) policy.Principal {
	return policy.Principal{IdentityID: id, ExpiresAt: time.Now().Add(time.Hour)}
}

// mustResolve builds a TenantContext the only supported way.
func mustResolve(t *testing.T, s policy.BindingStore, id, org, group string) policy.TenantContext {
	t.Helper()
	tc, err := policy.NewResolver(s, nil).Resolve(ctx, valid(id), org, group)
	if err != nil {
		t.Fatalf("resolve %s in %s/%s: %v", id, org, group, err)
	}
	return tc
}

func be(id string) policy.Resource  { return policy.Resource{Type: policy.ScopeBusinessEntity, ID: id} }
func grp(id string) policy.Resource { return policy.Resource{Type: policy.ScopeGroup, ID: id} }
func org(id string) policy.Resource { return policy.Resource{Type: policy.ScopeOrganization, ID: id} }

// expect asserts the exact decision (nil, ErrNotFound or ErrForbidden).
func expect(t *testing.T, e *policy.Evaluator, tc policy.TenantContext, c policy.Capability, r policy.Resource, want error) {
	t.Helper()
	got := e.Authorize(ctx, tc, c, r)
	if got != want { // sentinel identity: a denial must be exactly the sentinel
		t.Errorf("%s %s on %s %s: got %v, want %v", tc.IdentityID, c, r.Type, r.ID, got, want)
	}
}

// leakyStore returns bindings from every Organization, simulating a store
// bug; the evaluator must still ignore foreign bindings.
type leakyStore struct{ *policy.MemoryStore }

func (l leakyStore) BindingsForSubject(c context.Context, subject, _ string) ([]policy.Binding, error) {
	var out []policy.Binding
	for _, o := range []string{fixture.OrgAlpha, fixture.OrgBravo} {
		bs, err := l.MemoryStore.BindingsForSubject(c, subject, o)
		if err != nil {
			return nil, err
		}
		out = append(out, bs...)
	}
	return out, nil
}

func TestUnit_NoCrossOrganizationBleed(t *testing.T) {
	fixture.Label(t, fixture.Unit)
	s := policy.NewSeededMemoryStore()
	e := policy.NewEvaluator(s)
	alpha := mustResolve(t, s, fixture.IdentityAda, fixture.OrgAlpha, "")
	bravo := mustResolve(t, s, fixture.IdentityAda, fixture.OrgBravo, "")

	// Positive controls: ada is alpha org_owner and bravo org_member.
	expect(t, e, alpha, policy.CapGroupUpdate, grp(fixture.GroupGA1), nil)
	expect(t, e, alpha, policy.CapBusinessEntityProfileUpdate, be(fixture.BusinessEntityA), nil)
	expect(t, e, alpha, policy.CapAuditRead, org(fixture.OrgAlpha), nil)
	expect(t, e, bravo, policy.CapOrganizationRead, org(fixture.OrgBravo), nil)

	// Alpha authority never reaches bravo, and bravo context never sees alpha.
	expect(t, e, bravo, policy.CapGroupRead, grp(fixture.GroupGB1), policy.ErrNotFound)
	expect(t, e, bravo, policy.CapBusinessEntityProfileRead, be(fixture.BusinessEntityB), policy.ErrNotFound)
	expect(t, e, bravo, policy.CapAuditRead, org(fixture.OrgBravo), policy.ErrForbidden)
	expect(t, e, bravo, policy.CapOrganizationManageMembers, org(fixture.OrgBravo), policy.ErrForbidden)
	expect(t, e, bravo, policy.CapGroupRead, grp(fixture.GroupGA1), policy.ErrNotFound)
	expect(t, e, bravo, policy.CapBusinessEntityProfileRead, be(fixture.BusinessEntityA), policy.ErrNotFound)
	expect(t, e, bravo, policy.CapOrganizationRead, org(fixture.OrgAlpha), policy.ErrNotFound)
	expect(t, e, alpha, policy.CapBusinessEntityProfileRead, be(fixture.BusinessEntityB), policy.ErrNotFound)
	expect(t, e, alpha, policy.CapGroupRead, grp(fixture.GroupGB1), policy.ErrNotFound)

	caps, err := e.EffectiveCapabilities(ctx, bravo)
	if err != nil {
		t.Fatal(err)
	}
	if got := caps.Sorted(); !slices.Equal(got, []policy.Capability{policy.CapOrganizationRead}) {
		t.Errorf("bravo effective capabilities = %v, want only organization.read", got)
	}

	// A store that leaks the alpha org_owner binding into bravo evaluation
	// still contributes nothing in bravo.
	le := policy.NewEvaluator(leakyStore{s})
	expect(t, le, bravo, policy.CapAuditRead, org(fixture.OrgBravo), policy.ErrForbidden)
	expect(t, le, bravo, policy.CapGroupRead, grp(fixture.GroupGB1), policy.ErrNotFound)
	lcaps, err := le.EffectiveCapabilities(ctx, bravo)
	if err != nil {
		t.Fatal(err)
	}
	if len(lcaps) != 1 || !lcaps.Has(policy.CapOrganizationRead) {
		t.Errorf("leaky store: bravo capabilities = %v", lcaps.Sorted())
	}
}

func TestUnit_GroupAdminEditsProfileButNotSensitive(t *testing.T) {
	fixture.Label(t, fixture.Unit)
	s := policy.NewSeededMemoryStore()
	e := policy.NewEvaluator(s)
	lin := mustResolve(t, s, fixture.IdentityLin, fixture.OrgAlpha, "")
	expect(t, e, lin, policy.CapBusinessEntityProfileRead, be(fixture.BusinessEntityA), nil)
	expect(t, e, lin, policy.CapBusinessEntityProfileUpdate, be(fixture.BusinessEntityA), nil)
	expect(t, e, lin, policy.CapBusinessEntityOwnershipTransfer, be(fixture.BusinessEntityA), policy.ErrForbidden)
	expect(t, e, lin, policy.CapBusinessEntityPrivilegedManage, be(fixture.BusinessEntityA), policy.ErrForbidden)
	expect(t, e, lin, policy.CapBusinessEntitySelectOnboarding, be(fixture.BusinessEntityA), policy.ErrForbidden)
	expect(t, e, lin, policy.CapOrganizationManagePolicy, org(fixture.OrgAlpha), policy.ErrForbidden)
	expect(t, e, lin, policy.CapAuditRead, org(fixture.OrgAlpha), policy.ErrForbidden)

	sam := mustResolve(t, s, fixture.IdentitySam, fixture.OrgAlpha, "")
	expect(t, e, sam, policy.CapBusinessEntityProfileRead, be(fixture.BusinessEntityA), nil)
	expect(t, e, sam, policy.CapBusinessEntityProfileUpdate, be(fixture.BusinessEntityA), policy.ErrForbidden)
}

func TestUnit_SharedBusinessEntityGivesNoGroupVisibility(t *testing.T) {
	fixture.Label(t, fixture.Unit)
	s := policy.NewSeededMemoryStore()
	e := policy.NewEvaluator(s)
	lin := mustResolve(t, s, fixture.IdentityLin, fixture.OrgAlpha, "")

	// Positive controls on lin's own Group and the shared BusinessEntity.
	expect(t, e, lin, policy.CapGroupRead, grp(fixture.GroupGA1), nil)
	expect(t, e, lin, policy.CapGroupManageMembers, grp(fixture.GroupGA1), nil)
	expect(t, e, lin, policy.CapBusinessEntityProfileUpdate, be(fixture.BusinessEntityA), nil)

	// BE-A2 is linked to GA2 only.
	expect(t, e, lin, policy.CapBusinessEntityProfileUpdate, be(fixture.BusinessEntityA2), policy.ErrNotFound)
	expect(t, e, lin, policy.CapBusinessEntityProfileRead, be(fixture.BusinessEntityA2), policy.ErrNotFound)
	// GA2 shares BE-A with GA1, but GA2 and its membership stay invisible.
	expect(t, e, lin, policy.CapGroupRead, grp(fixture.GroupGA2), policy.ErrNotFound)
	expect(t, e, lin, policy.CapGroupManageMembers, grp(fixture.GroupGA2), policy.ErrNotFound)
	if err := e.AuthorizeRoleGrant(ctx, lin, grp(fixture.GroupGA2), policy.RoleGroupMember); err != policy.ErrNotFound {
		t.Errorf("lin managing GA2 members: %v, want ErrNotFound", err)
	}
	groups, err := policy.VisibleGroups(ctx, e, s, lin)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].ID != fixture.GroupGA1 {
		t.Errorf("lin visible groups = %+v, want only GA1", groups)
	}
	if _, err := policy.NewResolver(s, nil).Resolve(ctx, valid(fixture.IdentityLin), fixture.OrgAlpha, fixture.GroupGA2); err != policy.ErrNotFound {
		t.Errorf("lin selecting GA2: %v, want ErrNotFound", err)
	}
}

func TestUnit_DirectOwnerTransfersOwnership(t *testing.T) {
	fixture.Label(t, fixture.Unit)
	s := policy.NewSeededMemoryStore()
	e := policy.NewEvaluator(s)
	kai := mustResolve(t, s, fixture.IdentityKai, fixture.OrgAlpha, "")
	expect(t, e, kai, policy.CapBusinessEntityOwnershipTransfer, be(fixture.BusinessEntityA), nil)
	expect(t, e, kai, policy.CapBusinessEntityPrivilegedManage, be(fixture.BusinessEntityA), nil)
	// A direct grant covers exactly one BusinessEntity and no Group.
	expect(t, e, kai, policy.CapBusinessEntityOwnershipTransfer, be(fixture.BusinessEntityA2), policy.ErrNotFound)
	expect(t, e, kai, policy.CapGroupRead, grp(fixture.GroupGA1), policy.ErrNotFound)
	// Organization owners hold it too; Organization admins never do.
	ada := mustResolve(t, s, fixture.IdentityAda, fixture.OrgAlpha, "")
	expect(t, e, ada, policy.CapBusinessEntityOwnershipTransfer, be(fixture.BusinessEntityA), nil)
	if err := s.SetBindingRole(policy.SeedBindingAdaAlpha, policy.RoleOrgAdmin); err != nil {
		t.Fatal(err)
	}
	expect(t, e, ada, policy.CapBusinessEntityOwnershipTransfer, be(fixture.BusinessEntityA), policy.ErrForbidden)
}

func TestUnit_PolicyToggleRemovesInheritedEditing(t *testing.T) {
	fixture.Label(t, fixture.Unit)
	s := policy.NewSeededMemoryStore()
	e := policy.NewEvaluator(s)
	lin := mustResolve(t, s, fixture.IdentityLin, fixture.OrgAlpha, "")
	kai := mustResolve(t, s, fixture.IdentityKai, fixture.OrgAlpha, "")
	expect(t, e, lin, policy.CapBusinessEntityProfileUpdate, be(fixture.BusinessEntityA), nil)

	s.SetOrganizationPolicy(fixture.OrgAlpha, policy.OrganizationPolicy{GroupAdminsEditBusinessProfiles: false})
	expect(t, e, lin, policy.CapBusinessEntityProfileUpdate, be(fixture.BusinessEntityA), policy.ErrForbidden)
	expect(t, e, lin, policy.CapBusinessEntityProfileRead, be(fixture.BusinessEntityA), nil)   // read is not policy-bound
	expect(t, e, kai, policy.CapBusinessEntityProfileUpdate, be(fixture.BusinessEntityA), nil) // direct grants unaffected
	caps, err := e.EffectiveCapabilities(ctx, lin)
	if err != nil {
		t.Fatal(err)
	}
	if caps.Has(policy.CapBusinessEntityProfileUpdate) {
		t.Error("lin still holds business_entity.profile.update in the catalog with the policy off")
	}
	// The bravo policy is independent.
	bravoPolicy, _ := s.OrganizationPolicy(ctx, fixture.OrgBravo)
	if !bravoPolicy.GroupAdminsEditBusinessProfiles {
		t.Error("toggling alpha policy changed bravo")
	}

	s.SetOrganizationPolicy(fixture.OrgAlpha, policy.OrganizationPolicy{GroupAdminsEditBusinessProfiles: true})
	expect(t, e, lin, policy.CapBusinessEntityProfileUpdate, be(fixture.BusinessEntityA), nil)
}

func TestUnit_MemberRoleGrantEscalationDenied(t *testing.T) {
	fixture.Label(t, fixture.Unit)
	s := policy.NewSeededMemoryStore()
	e := policy.NewEvaluator(s)
	sam := mustResolve(t, s, fixture.IdentitySam, fixture.OrgAlpha, "")
	lin := mustResolve(t, s, fixture.IdentityLin, fixture.OrgAlpha, "")
	adaBravo := mustResolve(t, s, fixture.IdentityAda, fixture.OrgBravo, "")
	adaAlpha := mustResolve(t, s, fixture.IdentityAda, fixture.OrgAlpha, "")

	cases := []struct {
		name  string
		tc    policy.TenantContext
		scope policy.Resource
		role  policy.Role
		want  error
	}{
		{"group_admin grants member in own group (control)", lin, grp(fixture.GroupGA1), policy.RoleGroupMember, nil},
		{"org_owner grants org_admin (control)", adaAlpha, org(fixture.OrgAlpha), policy.RoleOrgAdmin, nil},
		{"group_member grants group_admin", sam, grp(fixture.GroupGA2), policy.RoleGroupAdmin, policy.ErrForbidden},
		{"group_member grants group_member", sam, grp(fixture.GroupGA2), policy.RoleGroupMember, policy.ErrForbidden},
		{"group_member grants org role", sam, org(fixture.OrgAlpha), policy.RoleOrgAdmin, policy.ErrForbidden},
		{"group_member grants business entity role", sam, be(fixture.BusinessEntityA), policy.RoleBEAdmin, policy.ErrForbidden},
		{"org_member grants org_admin", adaBravo, org(fixture.OrgBravo), policy.RoleOrgAdmin, policy.ErrForbidden},
		{"org_member grants in group", adaBravo, grp(fixture.GroupGB1), policy.RoleGroupAdmin, policy.ErrNotFound},
		{"group_admin grants org role", lin, org(fixture.OrgAlpha), policy.RoleOrgOwner, policy.ErrForbidden},
		{"group_admin grants business entity role", lin, be(fixture.BusinessEntityA), policy.RoleBEOwner, policy.ErrForbidden},
		{"group_admin role/scope mismatch", lin, grp(fixture.GroupGA1), policy.RoleOrgOwner, policy.ErrForbidden},
	}
	for _, c := range cases {
		if got := e.AuthorizeRoleGrant(ctx, c.tc, c.scope, c.role); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// An Organization admin may manage members but can never confer authority it
// lacks: no org_owner, no be_owner or be_admin (privileged settings), and it
// cannot change or revoke an existing org_owner either.
func TestUnit_AdminCannotEscalateThroughRoleGrants(t *testing.T) {
	fixture.Label(t, fixture.Unit)
	s := policy.NewSeededMemoryStore()
	e := policy.NewEvaluator(s)
	if err := s.SetBindingRole(policy.SeedBindingAdaAlpha, policy.RoleOrgAdmin); err != nil {
		t.Fatal(err)
	}
	admin := mustResolve(t, s, fixture.IdentityAda, fixture.OrgAlpha, "")
	cases := []struct {
		name  string
		scope policy.Resource
		role  policy.Role
		want  error
	}{
		{"org_admin grants org_member (control)", org(fixture.OrgAlpha), policy.RoleOrgMember, nil},
		{"org_admin grants org_admin (control: holds every org_admin capability)", org(fixture.OrgAlpha), policy.RoleOrgAdmin, nil},
		{"org_admin grants group_admin (control)", grp(fixture.GroupGA1), policy.RoleGroupAdmin, nil},
		{"org_admin grants org_owner", org(fixture.OrgAlpha), policy.RoleOrgOwner, policy.ErrForbidden},
		{"org_admin grants be_owner", be(fixture.BusinessEntityA), policy.RoleBEOwner, policy.ErrForbidden},
		{"org_admin grants be_admin (privileged settings)", be(fixture.BusinessEntityA), policy.RoleBEAdmin, policy.ErrForbidden},
	}
	for _, c := range cases {
		if got := e.AuthorizeRoleGrant(context.Background(), admin, c.scope, c.role); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	// Changing or revoking an existing org_owner binding authorizes against
	// the existing role, so an admin cannot demote or remove an owner.
	if got := e.AuthorizeRoleGrant(context.Background(), admin, org(fixture.OrgAlpha), policy.RoleOrgOwner); got != policy.ErrForbidden {
		t.Errorf("org_admin revoking org_owner: got %v, want ErrForbidden", got)
	}
}

func TestUnit_ForgedSelectorsMatchNonexistent(t *testing.T) {
	fixture.Label(t, fixture.Unit)
	s := policy.NewSeededMemoryStore()
	e := policy.NewEvaluator(s)
	r := policy.NewResolver(s, nil)
	lin := mustResolve(t, s, fixture.IdentityLin, fixture.OrgAlpha, "")

	// Positive control: the same call on lin's own Group succeeds.
	expect(t, e, lin, policy.CapGroupRead, grp(fixture.GroupGA1), nil)
	nonexistent := e.Authorize(ctx, lin, policy.CapGroupRead, grp(unknownID))
	if nonexistent != policy.ErrNotFound {
		t.Fatalf("nonexistent group: %v", nonexistent)
	}
	for name, id := range map[string]string{"foreign": fixture.GroupGB1, "invisible": fixture.GroupGA2, "malformed": "../" + fixture.GroupGA1, "empty": ""} {
		if got := e.Authorize(ctx, lin, policy.CapGroupRead, grp(id)); got != nonexistent {
			t.Errorf("%s group selector: %v, want identical to nonexistent (%v)", name, got, nonexistent)
		}
	}
	for name, id := range map[string]string{"foreign": fixture.BusinessEntityB, "nonexistent": unknownID} {
		if got := e.Authorize(ctx, lin, policy.CapBusinessEntityProfileRead, be(id)); got != nonexistent {
			t.Errorf("%s business entity: %v", name, got)
		}
	}

	// Context selectors: a foreign Organization (exists, no grant) and a
	// nonexistent one fail identically; so do foreign and unknown Groups.
	_, errUnknownOrg := r.Resolve(ctx, valid(fixture.IdentityLin), unknownID, "")
	_, errForeignOrg := r.Resolve(ctx, valid(fixture.IdentityLin), fixture.OrgBravo, "")
	_, errUnknownGroup := r.Resolve(ctx, valid(fixture.IdentityAda), fixture.OrgAlpha, unknownID)
	_, errForeignGroup := r.Resolve(ctx, valid(fixture.IdentityAda), fixture.OrgAlpha, fixture.GroupGB1)
	for name, err := range map[string]error{"unknown org": errUnknownOrg, "foreign org": errForeignOrg, "unknown group": errUnknownGroup, "foreign group": errForeignGroup} {
		if err != policy.ErrNotFound {
			t.Errorf("%s: %v, want ErrNotFound", name, err)
		}
	}
}

func TestUnit_AssociationValidationSameOrganization(t *testing.T) {
	fixture.Label(t, fixture.Unit)
	s := policy.NewSeededMemoryStore()
	e := policy.NewEvaluator(s)
	ada := mustResolve(t, s, fixture.IdentityAda, fixture.OrgAlpha, "")
	if err := e.ValidateAssociation(ctx, ada, fixture.GroupGA1, fixture.BusinessEntityA2); err != nil {
		t.Fatalf("same-Organization pair rejected: %v", err)
	}
	for name, pair := range map[string][2]string{
		"alpha group + bravo entity": {fixture.GroupGA1, fixture.BusinessEntityB},
		"bravo group + alpha entity": {fixture.GroupGB1, fixture.BusinessEntityA},
		"bravo pair from alpha":      {fixture.GroupGB1, fixture.BusinessEntityB},
		"unknown entity":             {fixture.GroupGA1, unknownID},
		"unknown group":              {unknownID, fixture.BusinessEntityA},
	} {
		if err := e.ValidateAssociation(ctx, ada, pair[0], pair[1]); err != policy.ErrNotFound {
			t.Errorf("%s: %v, want ErrNotFound", name, err)
		}
	}
	if err := e.ValidateAssociation(ctx, policy.TenantContext{}, fixture.GroupGA1, fixture.BusinessEntityA2); err != policy.ErrUnauthenticated {
		t.Errorf("empty context: %v", err)
	}
	// The store (like the composite foreign keys) is the second line.
	if err := s.Associate(fixture.OrgAlpha, fixture.GroupGA1, fixture.BusinessEntityB); err == nil {
		t.Error("memory store accepted a cross-Organization association")
	}
}

func TestUnit_OnboardingSelectionAndCompletion(t *testing.T) {
	fixture.Label(t, fixture.Unit)
	s := policy.NewSeededMemoryStore()
	e := policy.NewEvaluator(s)
	ada := mustResolve(t, s, fixture.IdentityAda, fixture.OrgAlpha, "")
	kai := mustResolve(t, s, fixture.IdentityKai, fixture.OrgAlpha, "")
	lin := mustResolve(t, s, fixture.IdentityLin, fixture.OrgAlpha, "")
	sam := mustResolve(t, s, fixture.IdentitySam, fixture.OrgAlpha, "")
	adaBravo := mustResolve(t, s, fixture.IdentityAda, fixture.OrgBravo, "")

	for name, c := range map[string]struct {
		tc   policy.TenantContext
		be   string
		want error
	}{
		"org_owner selects":          {ada, fixture.BusinessEntityA, nil},
		"be_owner selects":           {kai, fixture.BusinessEntityA, nil},
		"group_admin cannot select":  {lin, fixture.BusinessEntityA, policy.ErrForbidden},
		"group_member cannot select": {sam, fixture.BusinessEntityA, policy.ErrForbidden},
		"org_member cannot select":   {adaBravo, fixture.BusinessEntityB, policy.ErrNotFound},
		"foreign entity":             {ada, fixture.BusinessEntityB, policy.ErrNotFound},
	} {
		if got := e.SelectForOnboarding(ctx, c.tc, c.be); got != c.want {
			t.Errorf("selection %s: got %v, want %v", name, got, c.want)
		}
	}

	if err := e.CompleteOnboarding(ctx, ada, fixture.GroupGA1, fixture.BusinessEntityA); err != nil {
		t.Errorf("completion with association: %v", err)
	}
	if err := e.CompleteOnboarding(ctx, ada, fixture.GroupGA1, fixture.BusinessEntityA2); !errors.Is(err, policy.ErrAssociationMissing) {
		t.Errorf("completion without association: %v, want ErrAssociationMissing", err)
	}
	if err := s.Associate(fixture.OrgAlpha, fixture.GroupGA1, fixture.BusinessEntityA2); err != nil {
		t.Fatal(err)
	}
	if err := e.CompleteOnboarding(ctx, ada, fixture.GroupGA1, fixture.BusinessEntityA2); err != nil {
		t.Errorf("completion after association: %v", err)
	}
	if err := e.CompleteOnboarding(ctx, lin, fixture.GroupGA1, fixture.BusinessEntityA); err != policy.ErrForbidden {
		t.Errorf("group_admin completion: %v, want ErrForbidden", err)
	}
	if err := e.CompleteOnboarding(ctx, ada, fixture.GroupGB1, fixture.BusinessEntityA); err != policy.ErrNotFound {
		t.Errorf("cross-Organization completion: %v, want ErrNotFound", err)
	}
}

func TestUnit_TenantContextOnlyFromAuthenticatedIdentity(t *testing.T) {
	fixture.Label(t, fixture.Unit)
	s := policy.NewSeededMemoryStore()
	e := policy.NewEvaluator(s)
	now := time.Now()
	r := policy.NewResolver(s, func() time.Time { return now })

	for name, p := range map[string]policy.Principal{
		"anonymous":    {},
		"no expiry":    {IdentityID: fixture.IdentityAda},
		"expired":      {IdentityID: fixture.IdentityAda, ExpiresAt: now.Add(-time.Second)},
		"expiring now": {IdentityID: fixture.IdentityAda, ExpiresAt: now},
	} {
		if _, err := r.Resolve(ctx, p, fixture.OrgAlpha, ""); err != policy.ErrUnauthenticated {
			t.Errorf("%s principal: %v, want ErrUnauthenticated", name, err)
		}
	}

	tc, err := r.Resolve(ctx, valid(fixture.IdentityAda), fixture.OrgAlpha, fixture.GroupGA1)
	if err != nil || tc != (policy.TenantContext{IdentityID: fixture.IdentityAda, OrganizationID: fixture.OrgAlpha, GroupID: fixture.GroupGA1}) {
		t.Fatalf("ada alpha/GA1: %+v %v", tc, err)
	}
	// A Group grant alone establishes Organization context for that
	// Organization (lin), but only the granted Group can be selected.
	if _, err := r.Resolve(ctx, valid(fixture.IdentityLin), fixture.OrgAlpha, fixture.GroupGA1); err != nil {
		t.Errorf("lin alpha/GA1: %v", err)
	}
	if _, err := r.Resolve(ctx, valid(fixture.IdentityKai), fixture.OrgAlpha, fixture.GroupGA1); err != policy.ErrNotFound {
		t.Errorf("kai selecting GA1 without a group grant: %v", err)
	}
	// A hand-built context with no identity is never authorized.
	if err := e.Authorize(ctx, policy.TenantContext{OrganizationID: fixture.OrgAlpha}, policy.CapOrganizationRead, org(fixture.OrgAlpha)); err != policy.ErrUnauthenticated {
		t.Errorf("identity-less context: %v", err)
	}
	// Contexts follow current grants: after revocation lin cannot resolve alpha.
	if err := s.RemoveBinding(policy.SeedBindingLinGA1); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(ctx, valid(fixture.IdentityLin), fixture.OrgAlpha, ""); err != policy.ErrNotFound {
		t.Errorf("lin after revocation: %v, want ErrNotFound", err)
	}
}

func TestUnit_StorageObjectReadResolvesToScopeRead(t *testing.T) {
	fixture.Label(t, fixture.Unit)
	s := policy.NewSeededMemoryStore()
	e := policy.NewEvaluator(s)
	lin := mustResolve(t, s, fixture.IdentityLin, fixture.OrgAlpha, "")
	ada := mustResolve(t, s, fixture.IdentityAda, fixture.OrgAlpha, "")
	expect(t, e, lin, policy.CapStorageObjectRead, grp(fixture.GroupGA1), nil)
	expect(t, e, lin, policy.CapStorageObjectRead, grp(fixture.GroupGA2), policy.ErrNotFound)
	expect(t, e, lin, policy.CapStorageObjectRead, org(fixture.OrgAlpha), policy.ErrForbidden)
	expect(t, e, ada, policy.CapStorageObjectRead, org(fixture.OrgAlpha), nil)
	expect(t, e, ada, policy.CapStorageObjectRead, grp(fixture.GroupGB1), policy.ErrNotFound)
}
