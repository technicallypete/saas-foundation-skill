package policy

import (
	"context"
	"errors"
	"slices"
)

// Decision errors. Unknown resources, resources in another Organization and
// resources the caller cannot even read all yield ErrNotFound, so a forged
// or foreign identifier is indistinguishable from a nonexistent one.
var (
	ErrUnauthenticated = errors.New("policy: unauthenticated")
	ErrNotFound        = errors.New("policy: not found")
	ErrForbidden       = errors.New("policy: forbidden")
)

// IsDenial reports whether err is a policy decision (as opposed to a store
// or infrastructure failure).
func IsDenial(err error) bool {
	return errors.Is(err, ErrUnauthenticated) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden)
}

// Evaluator decides capabilities from the bindings in a BindingStore at
// evaluation time. It is deny by default and keeps no cache: every decision
// reads current bindings, associations and Organization policy.
type Evaluator struct {
	store BindingStore
}

// NewEvaluator returns an evaluator over store.
func NewEvaluator(store BindingStore) *Evaluator { return &Evaluator{store: store} }

// Allowed reports whether tc may exercise capability c on res. Denials are
// (false, nil); a non-nil error is a store failure.
func (e *Evaluator) Allowed(ctx context.Context, tc TenantContext, c Capability, res Resource) (bool, error) {
	err := e.Authorize(ctx, tc, c, res)
	switch {
	case err == nil:
		return true, nil
	case IsDenial(err):
		return false, nil
	default:
		return false, err
	}
}

// Authorize returns nil when tc may exercise c on res, ErrNotFound when res
// is unknown, foreign or invisible to the caller, ErrForbidden when res is
// visible but c is not held, and ErrUnauthenticated for an empty context.
func (e *Evaluator) Authorize(ctx context.Context, tc TenantContext, c Capability, res Resource) error {
	if tc.IdentityID == "" || tc.OrganizationID == "" {
		return ErrUnauthenticated
	}
	if err := e.resourceInContext(ctx, tc, res); err != nil {
		return err
	}
	if c == CapStorageObjectRead {
		// Not a role capability: the read capability of the owning scope.
		c = readCapability(res.Type)
	}
	if scope, ok := capabilityScope[c]; !ok || scope != res.Type {
		return ErrForbidden
	}
	ev, err := e.load(ctx, tc)
	if err != nil {
		return err
	}
	ok, err := ev.holds(ctx, c, res)
	if err != nil || ok {
		return err
	}
	if res.Type == ScopeOrganization {
		// The active Organization is the caller's own context; it is never
		// hidden from them, only forbidden.
		return ErrForbidden
	}
	// Existing Groups and BusinessEntities the caller cannot read are
	// reported exactly like unknown ones.
	rc := readCapability(res.Type)
	if rc == c {
		return ErrNotFound
	}
	visible, err := ev.holds(ctx, rc, res)
	if err != nil {
		return err
	}
	if !visible {
		return ErrNotFound
	}
	return ErrForbidden
}

// readCapability is the capability that makes a resource visible.
func readCapability(t ScopeType) Capability {
	switch t {
	case ScopeOrganization:
		return CapOrganizationRead
	case ScopeGroup:
		return CapGroupRead
	case ScopeBusinessEntity:
		return CapBusinessEntityProfileRead
	}
	return ""
}

// resourceInContext verifies that res exists and belongs to the active
// Organization. Every failure is the same ErrNotFound.
func (e *Evaluator) resourceInContext(ctx context.Context, tc TenantContext, res Resource) error {
	switch res.Type {
	case ScopeOrganization:
		if res.ID != tc.OrganizationID {
			return ErrNotFound
		}
		return nil
	case ScopeGroup, ScopeBusinessEntity:
		if res.ID == "" {
			return ErrNotFound
		}
		org, found, err := e.store.ResourceOrganization(ctx, res)
		if err != nil {
			return err
		}
		if !found || org != tc.OrganizationID {
			return ErrNotFound
		}
		return nil
	}
	return ErrNotFound
}

// evaluation holds the inputs of one decision, loaded lazily.
type evaluation struct {
	e        *Evaluator
	tc       TenantContext
	bindings []Binding
	policy   *OrganizationPolicy
}

func (e *Evaluator) load(ctx context.Context, tc TenantContext) (*evaluation, error) {
	all, err := e.store.BindingsForSubject(ctx, tc.IdentityID, tc.OrganizationID)
	if err != nil {
		return nil, err
	}
	ev := &evaluation{e: e, tc: tc}
	for _, b := range all {
		// A binding in another Organization never contributes, even if a
		// store returned it.
		if b.OrganizationID == tc.OrganizationID && b.SubjectID == tc.IdentityID && validBinding(b) {
			ev.bindings = append(ev.bindings, b)
		}
	}
	return ev, nil
}

func (ev *evaluation) orgPolicy(ctx context.Context) (OrganizationPolicy, error) {
	if ev.policy == nil {
		p, err := ev.e.store.OrganizationPolicy(ctx, ev.tc.OrganizationID)
		if err != nil {
			return OrganizationPolicy{}, err
		}
		ev.policy = &p
	}
	return *ev.policy, nil
}

// inherits reports whether a Group role inherits c for associated
// BusinessEntities under the current Organization policy.
func (ev *evaluation) inherits(ctx context.Context, r Role, c Capability) (bool, error) {
	if sensitiveCaps[c] {
		return false, nil
	}
	if groupInheritedCaps[r][c] {
		return true, nil
	}
	if groupInheritedIfPolicy[r][c] {
		p, err := ev.orgPolicy(ctx)
		if err != nil {
			return false, err
		}
		return p.GroupAdminsEditBusinessProfiles, nil
	}
	return false, nil
}

// holds evaluates c on res (already verified to be in the active
// Organization) against the loaded bindings.
func (ev *evaluation) holds(ctx context.Context, c Capability, res Resource) (bool, error) {
	var beGroups []string
	beGroupsLoaded := false
	for _, b := range ev.bindings {
		switch b.ScopeType {
		case ScopeOrganization:
			if orgRoleCaps[b.Role][c] {
				return true, nil
			}
		case ScopeGroup:
			if res.Type == ScopeGroup && res.ID == b.ScopeID && groupRoleCaps[b.Role][c] {
				return true, nil
			}
			if res.Type != ScopeBusinessEntity {
				continue
			}
			inh, err := ev.inherits(ctx, b.Role, c)
			if err != nil {
				return false, err
			}
			if !inh {
				continue
			}
			if !beGroupsLoaded {
				beGroups, err = ev.e.store.GroupsForBusinessEntity(ctx, ev.tc.OrganizationID, res.ID)
				if err != nil {
					return false, err
				}
				beGroupsLoaded = true
			}
			if slices.Contains(beGroups, b.ScopeID) {
				return true, nil
			}
		case ScopeBusinessEntity:
			if res.Type == ScopeBusinessEntity && res.ID == b.ScopeID && businessEntityRoleCaps[b.Role][c] {
				return true, nil
			}
		}
	}
	return false, nil
}

// CapabilitySet is a set of capabilities.
type CapabilitySet map[Capability]bool

// Has reports whether c is in the set.
func (s CapabilitySet) Has(c Capability) bool { return s[c] }

// Sorted returns the capabilities in a stable order.
func (s CapabilitySet) Sorted() []Capability {
	out := make([]Capability, 0, len(s))
	for c := range s {
		out = append(out, c)
	}
	slices.Sort(out)
	return out
}

// EffectiveCapabilities returns the capabilities tc holds on at least one
// resource of its Organization, for catalogs (tool discovery). It is a
// discovery aid, not an authorization decision: every action still calls
// Authorize for its exact resource. When tc has an active Group, Group-role
// capabilities come only from the binding on that Group.
func (e *Evaluator) EffectiveCapabilities(ctx context.Context, tc TenantContext) (CapabilitySet, error) {
	if tc.IdentityID == "" || tc.OrganizationID == "" {
		return CapabilitySet{}, nil
	}
	ev, err := e.load(ctx, tc)
	if err != nil {
		return nil, err
	}
	out := CapabilitySet{}
	for _, b := range ev.bindings {
		switch b.ScopeType {
		case ScopeOrganization:
			for c := range orgRoleCaps[b.Role] {
				out[c] = true
			}
		case ScopeGroup:
			if tc.GroupID != "" && b.ScopeID != tc.GroupID {
				continue
			}
			for c := range groupRoleCaps[b.Role] {
				out[c] = true
			}
			for _, c := range MatrixCapabilities {
				if capabilityScope[c] != ScopeBusinessEntity {
					continue
				}
				inh, err := ev.inherits(ctx, b.Role, c)
				if err != nil {
					return nil, err
				}
				if inh {
					out[c] = true
				}
			}
		case ScopeBusinessEntity:
			for c := range businessEntityRoleCaps[b.Role] {
				out[c] = true
			}
		}
	}
	return out, nil
}
