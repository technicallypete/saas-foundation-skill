package policy

import (
	"context"
	"errors"
	"slices"
	"time"
)

// Principal is an authenticated identity, produced only by an
// authenticator (session, bearer token). It carries no tenant scope.
type Principal struct {
	IdentityID string
	ExpiresAt  time.Time
}

// Valid reports whether the principal is authenticated and unexpired at now.
// A zero expiry is invalid (fail closed).
func (p Principal) Valid(now time.Time) bool {
	return p.IdentityID != "" && !p.ExpiresAt.IsZero() && now.Before(p.ExpiresAt)
}

// Resolver builds TenantContext values. It is the only supported
// constructor: a context exists only for an authenticated principal and an
// Organization (and optional Group) validated against current bindings.
type Resolver struct {
	store BindingStore
	eval  *Evaluator
	now   func() time.Time
}

// NewResolver returns a resolver over store. now may be nil (time.Now).
func NewResolver(store BindingStore, now func() time.Time) *Resolver {
	if now == nil {
		now = time.Now
	}
	return &Resolver{store: store, eval: NewEvaluator(store), now: now}
}

// Resolve validates the requested selectors for p and returns the trusted
// context. A forged, foreign or nonexistent Organization or Group yields the
// same ErrNotFound; an unauthenticated or expired principal yields
// ErrUnauthenticated.
func (r *Resolver) Resolve(ctx context.Context, p Principal, organizationID, groupID string) (TenantContext, error) {
	if !p.Valid(r.now()) {
		return TenantContext{}, ErrUnauthenticated
	}
	if organizationID == "" {
		return TenantContext{}, ErrNotFound
	}
	orgs, err := r.store.OrganizationsForSubject(ctx, p.IdentityID)
	if err != nil {
		return TenantContext{}, err
	}
	if !slices.Contains(orgs, organizationID) {
		return TenantContext{}, ErrNotFound
	}
	// Re-read the bindings: OrganizationsForSubject must agree with the
	// bindings actually held in that Organization.
	bs, err := r.store.BindingsForSubject(ctx, p.IdentityID, organizationID)
	if err != nil {
		return TenantContext{}, err
	}
	held := false
	for _, b := range bs {
		if b.OrganizationID == organizationID && b.SubjectID == p.IdentityID && validBinding(b) {
			held = true
			break
		}
	}
	if !held {
		return TenantContext{}, ErrNotFound
	}
	tc := TenantContext{IdentityID: p.IdentityID, OrganizationID: organizationID}
	if groupID != "" {
		// Selecting a Group requires a grant that makes it readable
		// (own-Group membership or an Organization role covering all Groups).
		if err := r.eval.Authorize(ctx, tc, CapGroupRead, Resource{Type: ScopeGroup, ID: groupID}); err != nil {
			if IsDenial(err) {
				return TenantContext{}, ErrNotFound
			}
			return TenantContext{}, err
		}
		tc.GroupID = groupID
	}
	return tc, nil
}

// GroupInfo describes a Group for listings.
type GroupInfo struct {
	ID             string
	OrganizationID string
	Slug           string
	Name           string
}

// GroupDirectory is an optional store extension that enumerates an
// Organization's Groups (filtered further by the evaluator).
type GroupDirectory interface {
	GroupsInOrganization(ctx context.Context, organizationID string) ([]GroupInfo, error)
}

// VisibleGroups returns the Groups of the active Organization that tc may
// read. A Group never becomes visible through a shared BusinessEntity.
func VisibleGroups(ctx context.Context, e *Evaluator, dir GroupDirectory, tc TenantContext) ([]GroupInfo, error) {
	all, err := dir.GroupsInOrganization(ctx, tc.OrganizationID)
	if err != nil {
		return nil, err
	}
	var out []GroupInfo
	for _, g := range all {
		ok, err := e.Allowed(ctx, tc, CapGroupRead, Resource{Type: ScopeGroup, ID: g.ID})
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, g)
		}
	}
	return out, nil
}

// ErrAssociationMissing is returned when onboarding completes without the
// GroupBusinessEntity association.
var ErrAssociationMissing = errors.New("policy: group business entity association missing")

// ValidateAssociation is the service-level same-Organization check for a
// GroupBusinessEntity pair, run before any database write (the composite
// foreign keys are the second line). Both sides must exist in the active
// Organization; anything else is ErrNotFound, so a cross-Organization pair
// is indistinguishable from a nonexistent one.
func (e *Evaluator) ValidateAssociation(ctx context.Context, tc TenantContext, groupID, businessEntityID string) error {
	if tc.IdentityID == "" || tc.OrganizationID == "" {
		return ErrUnauthenticated
	}
	g := Resource{Type: ScopeGroup, ID: groupID}
	be := Resource{Type: ScopeBusinessEntity, ID: businessEntityID}
	gOrg, gFound, err := e.store.ResourceOrganization(ctx, g)
	if err != nil {
		return err
	}
	beOrg, beFound, err := e.store.ResourceOrganization(ctx, be)
	if err != nil {
		return err
	}
	if !gFound || !beFound || gOrg != beOrg || gOrg != tc.OrganizationID {
		return ErrNotFound
	}
	return nil
}

// SelectForOnboarding authorizes selecting an existing BusinessEntity during
// onboarding (owner/admin authority: business_entity.select_for_onboarding).
func (e *Evaluator) SelectForOnboarding(ctx context.Context, tc TenantContext, businessEntityID string) error {
	return e.Authorize(ctx, tc, CapBusinessEntitySelectOnboarding, Resource{Type: ScopeBusinessEntity, ID: businessEntityID})
}

// CompleteOnboarding authorizes completing onboarding of groupID with an
// existing BusinessEntity: selection authority, a same-Organization pair,
// and an existing GroupBusinessEntity association.
func (e *Evaluator) CompleteOnboarding(ctx context.Context, tc TenantContext, groupID, businessEntityID string) error {
	if err := e.SelectForOnboarding(ctx, tc, businessEntityID); err != nil {
		return err
	}
	if err := e.ValidateAssociation(ctx, tc, groupID, businessEntityID); err != nil {
		return err
	}
	groups, err := e.store.GroupsForBusinessEntity(ctx, tc.OrganizationID, businessEntityID)
	if err != nil {
		return err
	}
	if !slices.Contains(groups, groupID) {
		return ErrAssociationMissing
	}
	return nil
}

// AuthorizeRoleGrant authorizes granting (or changing, or revoking) role at
// scope. The role must belong to the scope type, and the caller needs the
// member-management capability for that scope: organization.manage_members
// for Organization and BusinessEntity scopes, group.manage_members for a
// Group. Role grants are never inherited through a Group.
//
// No escalation: the caller must also already hold every capability the
// role confers at that scope, so an org_admin cannot grant org_owner or
// be_owner, and nobody can confer authority they lack. Callers changing or
// revoking a role authorize against the existing role as well.
func (e *Evaluator) AuthorizeRoleGrant(ctx context.Context, tc TenantContext, scope Resource, role Role) error {
	rs, ok := RoleScope(role)
	if !ok || rs != scope.Type {
		return ErrForbidden
	}
	var err error
	switch scope.Type {
	case ScopeOrganization:
		err = e.Authorize(ctx, tc, CapOrganizationManageMembers, scope)
	case ScopeGroup:
		err = e.Authorize(ctx, tc, CapGroupManageMembers, scope)
	case ScopeBusinessEntity:
		if err = e.resourceInContext(ctx, tc, scope); err == nil {
			err = e.Authorize(ctx, tc, CapOrganizationManageMembers, Resource{Type: ScopeOrganization, ID: tc.OrganizationID})
		}
	default:
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	return e.authorizeNoEscalation(ctx, tc, scope, role)
}

// authorizeNoEscalation requires the caller to hold every capability that
// role confers at scope.
func (e *Evaluator) authorizeNoEscalation(ctx context.Context, tc TenantContext, scope Resource, role Role) error {
	switch scope.Type {
	case ScopeOrganization:
		// An Organization role confers its capabilities Organization-wide, so
		// the caller needs them Organization-wide too: through its own
		// Organization bindings, not through a single Group or BusinessEntity.
		ev, err := e.load(ctx, tc)
		if err != nil {
			return err
		}
		held := map[Capability]bool{}
		for _, b := range ev.bindings {
			if b.ScopeType == ScopeOrganization {
				for c := range orgRoleCaps[b.Role] {
					held[c] = true
				}
			}
		}
		for c := range orgRoleCaps[role] {
			if !held[c] {
				return ErrForbidden
			}
		}
	case ScopeGroup:
		for c := range groupRoleCaps[role] {
			if err := e.Authorize(ctx, tc, c, scope); err != nil {
				return ErrForbidden
			}
		}
	case ScopeBusinessEntity:
		for c := range businessEntityRoleCaps[role] {
			if err := e.Authorize(ctx, tc, c, scope); err != nil {
				return ErrForbidden
			}
		}
	}
	return nil
}
