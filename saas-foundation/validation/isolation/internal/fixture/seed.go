// Package fixture is the frozen fixture API shared by every isolation test
// package: deterministic seed identifiers, environment, guarded connection
// helpers, tenant transactions and evidence labels.
package fixture

// Deterministic seed identifiers. sql/40_seed.sql inserts exactly these rows;
// tests reference them by name, never by literal.
const (
	OrgAlpha = "a0000000-0000-4000-8000-000000000001"
	OrgBravo = "b0000000-0000-4000-8000-000000000001"

	OrgAlphaSlug = "alpha"
	OrgBravoSlug = "bravo"

	GroupGA1 = "a1000000-0000-4000-8000-000000000001" // alpha, slug "ga-one"
	GroupGA2 = "a1000000-0000-4000-8000-000000000002" // alpha, slug "ga-two"
	GroupGB1 = "b1000000-0000-4000-8000-000000000001" // bravo, slug "gb-one"

	BusinessEntityA  = "a2000000-0000-4000-8000-000000000001" // alpha, linked to GA1 and GA2
	BusinessEntityA2 = "a2000000-0000-4000-8000-000000000002" // alpha, linked to GA2 only
	BusinessEntityB  = "b2000000-0000-4000-8000-000000000001" // bravo, linked to GB1

	IdentityAda = "c0000000-0000-4000-8000-000000000001" // ada@example.test: alpha org_owner, bravo org_member
	IdentityLin = "c0000000-0000-4000-8000-000000000002" // lin@example.test: GA1 group_admin only
	IdentitySam = "c0000000-0000-4000-8000-000000000003" // sam@example.test: GA2 group_member only
	IdentityKai = "c0000000-0000-4000-8000-000000000004" // kai@example.test: BE-A be_owner (direct_grant) only
	IdentityBea = "c0000000-0000-4000-8000-000000000005" // bea@example.test: bravo org_member only (never visible from alpha)

	// Seeded stored objects (keys are server-generated and opaque).
	ObjectAlphaGA1 = "a3000000-0000-4000-8000-000000000001"
	ObjectBravoGB1 = "b3000000-0000-4000-8000-000000000001"

	// AuditRetentionDays is the fixture retention window; sql/40_seed.sql
	// seeds one event per organization older than this window.
	AuditRetentionDays = 400
)

// Seeded row counts used by positive controls.
const (
	SeedGroupsAlpha           = 2
	SeedGroupsBravo           = 1
	SeedBusinessEntitiesAlpha = 2
	SeedBusinessEntitiesBravo = 1
)
