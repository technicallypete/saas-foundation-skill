// Package policy is the fixture's shared RoleBinding policy system:
// deny-by-default capabilities derived server-side from scoped bindings,
// capability-specific Group -> BusinessEntity inheritance computed at
// evaluation time, and Organization policy. There are no mirrored rows.
//
// This file is the frozen public contract (owned by the integrator). The
// evaluator, stores and tests are implemented alongside it.
package policy

import "context"

// ScopeType is the kind of scope a binding applies to.
type ScopeType string

const (
	ScopeOrganization   ScopeType = "organization"
	ScopeGroup          ScopeType = "group"
	ScopeBusinessEntity ScopeType = "business_entity"
)

// Role is a scoped role. Roles are only meaningful at their scope type.
type Role string

const (
	RoleOrgOwner    Role = "org_owner"
	RoleOrgAdmin    Role = "org_admin"
	RoleOrgMember   Role = "org_member"
	RoleGroupAdmin  Role = "group_admin"
	RoleGroupMember Role = "group_member"
	RoleBEOwner     Role = "be_owner"
	RoleBEAdmin     Role = "be_admin"
)

// Source records how a binding arose. Membership roles are bindings with
// source "membership"; the membership entities are projections of them.
type Source string

const (
	SourceMembership  Source = "membership"
	SourceDirectGrant Source = "direct_grant"
)

// Capability is an action the policy system can allow. Deny by default.
type Capability string

const (
	CapOrganizationRead                Capability = "organization.read"
	CapOrganizationManageMembers       Capability = "organization.manage_members"
	CapOrganizationManagePolicy        Capability = "organization.manage_policy"
	CapGroupRead                       Capability = "group.read"
	CapGroupUpdate                     Capability = "group.update"
	CapGroupManageMembers              Capability = "group.manage_members"
	CapBusinessEntityProfileRead       Capability = "business_entity.profile.read"
	CapBusinessEntityProfileUpdate     Capability = "business_entity.profile.update"
	CapBusinessEntitySelectOnboarding  Capability = "business_entity.select_for_onboarding"
	CapBusinessEntityOwnershipTransfer Capability = "business_entity.ownership.transfer"
	CapBusinessEntityPrivilegedManage  Capability = "business_entity.privileged_settings.manage"
	CapAuditRead                       Capability = "audit.read"
	CapStorageObjectRead               Capability = "storage.object.read"
)

// Binding is one row of the single mutable authority store
// RoleBinding(subject, scope_type, scope_id, role, source).
type Binding struct {
	ID             string
	OrganizationID string
	SubjectID      string
	ScopeType      ScopeType
	ScopeID        string
	Role           Role
	Source         Source
}

// OrganizationPolicy is Organization-level policy that modulates inheritance.
type OrganizationPolicy struct {
	// GroupAdminsEditBusinessProfiles allows group_admin to inherit
	// business_entity.profile.update through an associated Group.
	GroupAdminsEditBusinessProfiles bool
}

// Resource identifies an object a capability is evaluated against.
type Resource struct {
	Type ScopeType
	ID   string
}

// TenantContext is the trusted, server-resolved security envelope for one
// request. It is never persisted and never built from client-supplied
// identifiers alone; construct it only through the policy package's
// resolver, which validates membership against the BindingStore.
type TenantContext struct {
	IdentityID     string
	OrganizationID string
	GroupID        string // optional; empty means Organization scope only
}

// BindingStore reads authority data. Implementations: an in-memory store
// for unit tests and a PostgreSQL store that reads role_bindings and the
// association tables through the runtime role under tenant context.
type BindingStore interface {
	// BindingsForSubject returns the subject's bindings inside one Organization.
	BindingsForSubject(ctx context.Context, subjectID, organizationID string) ([]Binding, error)
	// OrganizationsForSubject returns the Organizations where the subject
	// holds any binding.
	OrganizationsForSubject(ctx context.Context, subjectID string) ([]string, error)
	// GroupsForBusinessEntity returns the Groups associated with a
	// BusinessEntity through GroupBusinessEntity.
	GroupsForBusinessEntity(ctx context.Context, organizationID, businessEntityID string) ([]string, error)
	// ResourceOrganization returns the owning Organization of a Group or
	// BusinessEntity; found is false for unknown or invisible resources.
	ResourceOrganization(ctx context.Context, r Resource) (organizationID string, found bool, err error)
	// OrganizationPolicy returns the Organization's policy.
	OrganizationPolicy(ctx context.Context, organizationID string) (OrganizationPolicy, error)
}
