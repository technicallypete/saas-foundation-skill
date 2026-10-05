package policy

// The fixture permission matrix (references/tenancy-authorization.md,
// "Permission matrix"). These tables are the only place roles map to
// capabilities; TestUnit_MatrixMatchesReference keeps them equal to the
// reference document.

// capabilityScope is the resource type each capability is evaluated against.
var capabilityScope = map[Capability]ScopeType{
	CapOrganizationRead:                ScopeOrganization,
	CapOrganizationManageMembers:       ScopeOrganization,
	CapOrganizationManagePolicy:        ScopeOrganization,
	CapAuditRead:                       ScopeOrganization,
	CapGroupRead:                       ScopeGroup,
	CapGroupUpdate:                     ScopeGroup,
	CapGroupManageMembers:              ScopeGroup,
	CapBusinessEntityProfileRead:       ScopeBusinessEntity,
	CapBusinessEntityProfileUpdate:     ScopeBusinessEntity,
	CapBusinessEntitySelectOnboarding:  ScopeBusinessEntity,
	CapBusinessEntityOwnershipTransfer: ScopeBusinessEntity,
	CapBusinessEntityPrivilegedManage:  ScopeBusinessEntity,
}

// MatrixCapabilities lists the capabilities that appear as matrix rows, in
// reference order. storage.object.read is not a role row: it resolves to the
// read capability of the object's owning scope.
var MatrixCapabilities = []Capability{
	CapOrganizationRead,
	CapOrganizationManageMembers,
	CapOrganizationManagePolicy,
	CapGroupRead,
	CapGroupUpdate,
	CapGroupManageMembers,
	CapBusinessEntityProfileRead,
	CapBusinessEntityProfileUpdate,
	CapBusinessEntitySelectOnboarding,
	CapBusinessEntityOwnershipTransfer,
	CapBusinessEntityPrivilegedManage,
	CapAuditRead,
}

// MatrixRoles lists the role columns in reference order.
var MatrixRoles = []Role{RoleOrgOwner, RoleOrgAdmin, RoleOrgMember, RoleGroupAdmin, RoleGroupMember, RoleBEOwner, RoleBEAdmin}

func capSet(caps ...Capability) map[Capability]bool {
	m := make(map[Capability]bool, len(caps))
	for _, c := range caps {
		m[c] = true
	}
	return m
}

// orgRoleCaps: an Organization role applies to every Group and
// BusinessEntity of that Organization ("✓" / "✓ all").
var orgRoleCaps = map[Role]map[Capability]bool{
	RoleOrgOwner: capSet(
		CapOrganizationRead, CapOrganizationManageMembers, CapOrganizationManagePolicy,
		CapGroupRead, CapGroupUpdate, CapGroupManageMembers,
		CapBusinessEntityProfileRead, CapBusinessEntityProfileUpdate, CapBusinessEntitySelectOnboarding,
		CapBusinessEntityOwnershipTransfer, CapBusinessEntityPrivilegedManage,
		CapAuditRead,
	),
	RoleOrgAdmin: capSet(
		CapOrganizationRead, CapOrganizationManageMembers,
		CapGroupRead, CapGroupUpdate, CapGroupManageMembers,
		CapBusinessEntityProfileRead, CapBusinessEntityProfileUpdate, CapBusinessEntitySelectOnboarding,
	),
	RoleOrgMember: capSet(CapOrganizationRead),
}

// groupRoleCaps apply only to the Group where the role is held ("✓ own").
var groupRoleCaps = map[Role]map[Capability]bool{
	RoleGroupAdmin:  capSet(CapGroupRead, CapGroupUpdate, CapGroupManageMembers),
	RoleGroupMember: capSet(CapGroupRead),
}

// groupInheritedCaps are inherited by a Group role for BusinessEntities
// associated with that Group ("✓ via Group"). Sensitive capabilities never
// appear here. Policy-conditional inheritance is listed separately.
var groupInheritedCaps = map[Role]map[Capability]bool{
	RoleGroupAdmin:  capSet(CapBusinessEntityProfileRead),
	RoleGroupMember: capSet(CapBusinessEntityProfileRead),
}

// groupInheritedIfPolicy are inherited only while Organization policy
// group_admins_edit_business_profiles is on ("✓ via Group if policy").
var groupInheritedIfPolicy = map[Role]map[Capability]bool{
	RoleGroupAdmin: capSet(CapBusinessEntityProfileUpdate),
}

// businessEntityRoleCaps apply to the directly granted BusinessEntity only.
var businessEntityRoleCaps = map[Role]map[Capability]bool{
	RoleBEOwner: capSet(
		CapBusinessEntityProfileRead, CapBusinessEntityProfileUpdate, CapBusinessEntitySelectOnboarding,
		CapBusinessEntityOwnershipTransfer, CapBusinessEntityPrivilegedManage,
	),
	RoleBEAdmin: capSet(
		CapBusinessEntityProfileRead, CapBusinessEntityProfileUpdate, CapBusinessEntitySelectOnboarding,
		CapBusinessEntityPrivilegedManage,
	),
}

// sensitiveCaps are never inherited through a Group, whatever the tables
// above say; inheritance is additionally filtered through this list.
var sensitiveCaps = capSet(
	CapBusinessEntityOwnershipTransfer, CapBusinessEntityPrivilegedManage,
	CapOrganizationManagePolicy, CapAuditRead,
	CapOrganizationManageMembers, CapGroupManageMembers,
)

// RoleScope returns the scope type a role is meaningful at.
func RoleScope(r Role) (ScopeType, bool) {
	switch {
	case orgRoleCaps[r] != nil:
		return ScopeOrganization, true
	case groupRoleCaps[r] != nil:
		return ScopeGroup, true
	case businessEntityRoleCaps[r] != nil:
		return ScopeBusinessEntity, true
	}
	return "", false
}

// validBinding reports whether a binding has a role/scope/source combination
// the authority store allows. Anything else contributes nothing.
func validBinding(b Binding) bool {
	scope, ok := RoleScope(b.Role)
	if !ok || scope != b.ScopeType || b.ScopeID == "" {
		return false
	}
	switch b.ScopeType {
	case ScopeOrganization:
		return b.Source == SourceMembership && b.ScopeID == b.OrganizationID
	case ScopeGroup:
		return b.Source == SourceMembership
	case ScopeBusinessEntity:
		return b.Source == SourceMembership || b.Source == SourceDirectGrant
	}
	return false
}
