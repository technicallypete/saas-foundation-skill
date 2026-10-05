package policy

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"example.invalid/archetype/isolation/internal/fixture"
)

// ErrStoreNotFound is returned by MemoryStore mutations for unknown rows.
var ErrStoreNotFound = errors.New("memory store: not found")

// BusinessEntityInfo describes a BusinessEntity in the in-memory store.
type BusinessEntityInfo struct {
	ID             string
	OrganizationID string
	Name           string
	Description    string
}

// OrganizationInfo describes an Organization in the in-memory store.
type OrganizationInfo struct {
	ID   string
	Slug string
	Name string
}

// MemoryStore is the in-memory BindingStore for unit tests. Like the SQL
// schema it keeps bindings as the only role store; associations,
// Organization policy and a small amount of domain data (names, profiles)
// live beside them. Every mutation advances Version, the policy/version
// marker MCP sessions compare on each request.
type MemoryStore struct {
	mu         sync.Mutex
	version    uint64
	nextID     int
	identities map[string]string // id -> email
	orgs       map[string]OrganizationInfo
	policies   map[string]OrganizationPolicy
	groups     map[string]GroupInfo
	entities   map[string]BusinessEntityInfo
	assocs     map[[3]string]bool // (org, group, be)
	bindings   []Binding
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		identities: map[string]string{},
		orgs:       map[string]OrganizationInfo{},
		policies:   map[string]OrganizationPolicy{},
		groups:     map[string]GroupInfo{},
		entities:   map[string]BusinessEntityInfo{},
		assocs:     map[[3]string]bool{},
	}
}

// Seed binding IDs of NewSeededMemoryStore (the SQL seed uses generated
// UUIDs; these exist only in memory).
const (
	SeedBindingAdaAlpha = "rb-ada-alpha-org-owner"
	SeedBindingAdaBravo = "rb-ada-bravo-org-member"
	SeedBindingLinGA1   = "rb-lin-ga1-group-admin"
	SeedBindingSamGA2   = "rb-sam-ga2-group-member"
	SeedBindingKaiBEA   = "rb-kai-bea-be-owner"
)

// NewSeededMemoryStore mirrors sql/40_seed.sql using the fixture seed IDs.
func NewSeededMemoryStore() *MemoryStore {
	s := NewMemoryStore()
	s.AddIdentity(fixture.IdentityAda, "ada@example.test")
	s.AddIdentity(fixture.IdentityLin, "lin@example.test")
	s.AddIdentity(fixture.IdentitySam, "sam@example.test")
	s.AddIdentity(fixture.IdentityKai, "kai@example.test")
	s.AddOrganization(fixture.OrgAlpha, fixture.OrgAlphaSlug, "Org Alpha")
	s.AddOrganization(fixture.OrgBravo, fixture.OrgBravoSlug, "Org Bravo")
	s.AddGroup(fixture.GroupGA1, fixture.OrgAlpha, "ga-one", "Group A One")
	s.AddGroup(fixture.GroupGA2, fixture.OrgAlpha, "ga-two", "Group A Two")
	s.AddGroup(fixture.GroupGB1, fixture.OrgBravo, "gb-one", "Group B One")
	s.AddBusinessEntity(fixture.BusinessEntityA, fixture.OrgAlpha, "Business A", "shared by GA1 and GA2")
	s.AddBusinessEntity(fixture.BusinessEntityA2, fixture.OrgAlpha, "Business A2", "GA2 only")
	s.AddBusinessEntity(fixture.BusinessEntityB, fixture.OrgBravo, "Business B", "")
	s.Associate(fixture.OrgAlpha, fixture.GroupGA1, fixture.BusinessEntityA)
	s.Associate(fixture.OrgAlpha, fixture.GroupGA2, fixture.BusinessEntityA)
	s.Associate(fixture.OrgAlpha, fixture.GroupGA2, fixture.BusinessEntityA2)
	s.Associate(fixture.OrgBravo, fixture.GroupGB1, fixture.BusinessEntityB)
	mustAdd := func(b Binding) {
		if _, err := s.AddBinding(b); err != nil {
			panic(err)
		}
	}
	mustAdd(Binding{ID: SeedBindingAdaAlpha, OrganizationID: fixture.OrgAlpha, SubjectID: fixture.IdentityAda, ScopeType: ScopeOrganization, ScopeID: fixture.OrgAlpha, Role: RoleOrgOwner, Source: SourceMembership})
	mustAdd(Binding{ID: SeedBindingAdaBravo, OrganizationID: fixture.OrgBravo, SubjectID: fixture.IdentityAda, ScopeType: ScopeOrganization, ScopeID: fixture.OrgBravo, Role: RoleOrgMember, Source: SourceMembership})
	mustAdd(Binding{ID: SeedBindingLinGA1, OrganizationID: fixture.OrgAlpha, SubjectID: fixture.IdentityLin, ScopeType: ScopeGroup, ScopeID: fixture.GroupGA1, Role: RoleGroupAdmin, Source: SourceMembership})
	mustAdd(Binding{ID: SeedBindingSamGA2, OrganizationID: fixture.OrgAlpha, SubjectID: fixture.IdentitySam, ScopeType: ScopeGroup, ScopeID: fixture.GroupGA2, Role: RoleGroupMember, Source: SourceMembership})
	mustAdd(Binding{ID: SeedBindingKaiBEA, OrganizationID: fixture.OrgAlpha, SubjectID: fixture.IdentityKai, ScopeType: ScopeBusinessEntity, ScopeID: fixture.BusinessEntityA, Role: RoleBEOwner, Source: SourceDirectGrant})
	return s
}

func (s *MemoryStore) bump() { s.version++ }

// Version is the store-level policy/version marker. It changes whenever a
// binding, association or Organization policy changes.
func (s *MemoryStore) Version(context.Context) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version, nil
}

// AddIdentity registers a global identity.
func (s *MemoryStore) AddIdentity(id, email string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.identities[id] = email
}

// IdentityEmail returns an identity's email.
func (s *MemoryStore) IdentityEmail(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.identities[id]
	return e, ok
}

// AddOrganization registers an Organization with the default policy
// (group_admins_edit_business_profiles on).
func (s *MemoryStore) AddOrganization(id, slug, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orgs[id] = OrganizationInfo{ID: id, Slug: slug, Name: name}
	s.policies[id] = OrganizationPolicy{GroupAdminsEditBusinessProfiles: true}
	s.bump()
}

// Organization returns an Organization's description.
func (s *MemoryStore) Organization(id string) (OrganizationInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orgs[id]
	return o, ok
}

// OrganizationIDBySlug resolves an Organization slug (a selector only).
func (s *MemoryStore) OrganizationIDBySlug(slug string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.orgs {
		if o.Slug == slug {
			return o.ID, true
		}
	}
	return "", false
}

// AddGroup registers a Group.
func (s *MemoryStore) AddGroup(id, orgID, slug, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.groups[id] = GroupInfo{ID: id, OrganizationID: orgID, Slug: slug, Name: name}
	s.bump()
}

// GroupIDBySlug resolves a Group slug within one Organization (a selector
// only). Slugs are unique per Organization, never global.
func (s *MemoryStore) GroupIDBySlug(orgID, slug string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.groups {
		if g.OrganizationID == orgID && g.Slug == slug {
			return g.ID, true
		}
	}
	return "", false
}

// Group returns a Group's description.
func (s *MemoryStore) Group(id string) (GroupInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[id]
	return g, ok
}

// RenameGroup changes a Group's display name (an administrative mutation).
func (s *MemoryStore) RenameGroup(id, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[id]
	if !ok {
		return ErrStoreNotFound
	}
	g.Name = name
	s.groups[id] = g
	return nil
}

// GroupsInOrganization implements GroupDirectory.
func (s *MemoryStore) GroupsInOrganization(_ context.Context, orgID string) ([]GroupInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []GroupInfo
	for _, g := range s.groups {
		if g.OrganizationID == orgID {
			out = append(out, g)
		}
	}
	slices.SortFunc(out, func(a, b GroupInfo) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

// AddBusinessEntity registers a BusinessEntity.
func (s *MemoryStore) AddBusinessEntity(id, orgID, name, description string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entities[id] = BusinessEntityInfo{ID: id, OrganizationID: orgID, Name: name, Description: description}
	s.bump()
}

// BusinessEntity returns a BusinessEntity's description.
func (s *MemoryStore) BusinessEntity(id string) (BusinessEntityInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entities[id]
	return e, ok
}

// UpdateBusinessEntityDescription changes a BusinessEntity profile field.
func (s *MemoryStore) UpdateBusinessEntityDescription(id, description string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entities[id]
	if !ok {
		return ErrStoreNotFound
	}
	e.Description = description
	s.entities[id] = e
	return nil
}

// Associate adds a GroupBusinessEntity association. Like the composite
// foreign keys, it refuses cross-Organization pairs.
func (s *MemoryStore) Associate(orgID, groupID, beID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, gok := s.groups[groupID]
	e, eok := s.entities[beID]
	if !gok || !eok || g.OrganizationID != orgID || e.OrganizationID != orgID {
		return fmt.Errorf("associate: %w", ErrStoreNotFound)
	}
	s.assocs[[3]string{orgID, groupID, beID}] = true
	s.bump()
	return nil
}

// SetOrganizationPolicy replaces an Organization's policy.
func (s *MemoryStore) SetOrganizationPolicy(orgID string, p OrganizationPolicy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policies[orgID] = p
	s.bump()
}

// AddBinding inserts one binding (one row). It enforces the schema's rules:
// valid role/scope/source, same-Organization scope, and at most one
// membership role per subject and scope. An empty ID is generated.
func (s *MemoryStore) AddBinding(b Binding) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validBinding(b) {
		return "", fmt.Errorf("add binding: invalid role/scope/source %+v", b)
	}
	if _, ok := s.orgs[b.OrganizationID]; !ok {
		return "", fmt.Errorf("add binding: %w", ErrStoreNotFound)
	}
	switch b.ScopeType {
	case ScopeGroup:
		if g, ok := s.groups[b.ScopeID]; !ok || g.OrganizationID != b.OrganizationID {
			return "", fmt.Errorf("add binding: group %w", ErrStoreNotFound)
		}
	case ScopeBusinessEntity:
		if e, ok := s.entities[b.ScopeID]; !ok || e.OrganizationID != b.OrganizationID {
			return "", fmt.Errorf("add binding: business entity %w", ErrStoreNotFound)
		}
	}
	for _, x := range s.bindings {
		if x.SubjectID == b.SubjectID && x.ScopeType == b.ScopeType && x.ScopeID == b.ScopeID &&
			x.Source == SourceMembership && b.Source == SourceMembership {
			return "", fmt.Errorf("add binding: duplicate membership role for subject at scope")
		}
	}
	if b.ID == "" {
		s.nextID++
		b.ID = fmt.Sprintf("rb-generated-%d", s.nextID)
	}
	s.bindings = append(s.bindings, b)
	s.bump()
	return b.ID, nil
}

// RemoveBinding deletes exactly one binding.
func (s *MemoryStore) RemoveBinding(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.bindings, func(b Binding) bool { return b.ID == id })
	if i < 0 {
		return ErrStoreNotFound
	}
	s.bindings = slices.Delete(s.bindings, i, i+1)
	s.bump()
	return nil
}

// SetBindingRole changes the role of one binding (one row), keeping its
// scope. The new role must be valid at that scope.
func (s *MemoryStore) SetBindingRole(id string, role Role) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.bindings, func(b Binding) bool { return b.ID == id })
	if i < 0 {
		return ErrStoreNotFound
	}
	nb := s.bindings[i]
	nb.Role = role
	if !validBinding(nb) {
		return fmt.Errorf("set binding role: %s is not valid at %s scope", role, nb.ScopeType)
	}
	s.bindings[i] = nb
	s.bump()
	return nil
}

// FindMembership returns the membership binding of subject at a scope.
func (s *MemoryStore) FindMembership(subjectID string, scope Resource) (Binding, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.bindings {
		if b.SubjectID == subjectID && b.ScopeType == scope.Type && b.ScopeID == scope.ID && b.Source == SourceMembership {
			return b, true
		}
	}
	return Binding{}, false
}

// TransferBusinessEntityOwnership replaces the be_owner grant(s) of a
// BusinessEntity with one direct grant to newOwnerID.
func (s *MemoryStore) TransferBusinessEntityOwnership(orgID, beID, newOwnerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entities[beID]
	if !ok || e.OrganizationID != orgID {
		return ErrStoreNotFound
	}
	if _, ok := s.identities[newOwnerID]; !ok {
		return ErrStoreNotFound
	}
	s.bindings = slices.DeleteFunc(s.bindings, func(b Binding) bool {
		return b.ScopeType == ScopeBusinessEntity && b.ScopeID == beID && b.Role == RoleBEOwner
	})
	s.nextID++
	s.bindings = append(s.bindings, Binding{
		ID: fmt.Sprintf("rb-generated-%d", s.nextID), OrganizationID: orgID, SubjectID: newOwnerID,
		ScopeType: ScopeBusinessEntity, ScopeID: beID, Role: RoleBEOwner, Source: SourceDirectGrant,
	})
	s.bump()
	return nil
}

// BindingsForSubject implements BindingStore.
func (s *MemoryStore) BindingsForSubject(_ context.Context, subjectID, organizationID string) ([]Binding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Binding
	for _, b := range s.bindings {
		if b.SubjectID == subjectID && b.OrganizationID == organizationID {
			out = append(out, b)
		}
	}
	return out, nil
}

// OrganizationsForSubject implements BindingStore.
func (s *MemoryStore) OrganizationsForSubject(_ context.Context, subjectID string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, b := range s.bindings {
		if b.SubjectID == subjectID && !slices.Contains(out, b.OrganizationID) {
			out = append(out, b.OrganizationID)
		}
	}
	slices.Sort(out)
	return out, nil
}

// GroupsForBusinessEntity implements BindingStore.
func (s *MemoryStore) GroupsForBusinessEntity(_ context.Context, organizationID, businessEntityID string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for k := range s.assocs {
		if k[0] == organizationID && k[2] == businessEntityID {
			out = append(out, k[1])
		}
	}
	slices.Sort(out)
	return out, nil
}

// ResourceOrganization implements BindingStore.
func (s *MemoryStore) ResourceOrganization(_ context.Context, r Resource) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.Type {
	case ScopeOrganization:
		if _, ok := s.orgs[r.ID]; ok {
			return r.ID, true, nil
		}
	case ScopeGroup:
		if g, ok := s.groups[r.ID]; ok {
			return g.OrganizationID, true, nil
		}
	case ScopeBusinessEntity:
		if e, ok := s.entities[r.ID]; ok {
			return e.OrganizationID, true, nil
		}
	}
	return "", false, nil
}

// OrganizationPolicy implements BindingStore. Unknown Organizations get
// the default policy.
func (s *MemoryStore) OrganizationPolicy(_ context.Context, organizationID string) (OrganizationPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.policies[organizationID]; ok {
		return p, nil
	}
	return OrganizationPolicy{GroupAdminsEditBusinessProfiles: true}, nil
}

var (
	_ BindingStore   = (*MemoryStore)(nil)
	_ GroupDirectory = (*MemoryStore)(nil)
)
