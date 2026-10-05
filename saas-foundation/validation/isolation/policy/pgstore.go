package policy

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Querier is the subset of pgx.Tx the PostgreSQL store needs.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// PGStore reads authority data from PostgreSQL through the runtime role.
// It must run inside a transaction that already carries transaction-local
// tenant context (fixture.SetTenant / WithTenantTx), so row-level security
// limits every read: bindings come only from role_bindings (the single
// role store), associations from group_business_entities, ownership from
// groups / business_entities and policy from organization_policies.
type PGStore struct {
	q Querier
}

// NewPGStore returns a store reading through q (normally a pgx.Tx).
func NewPGStore(q Querier) *PGStore { return &PGStore{q: q} }

// BindingsForSubject implements BindingStore.
func (s *PGStore) BindingsForSubject(ctx context.Context, subjectID, organizationID string) ([]Binding, error) {
	if !looksLikeUUID(subjectID) || !looksLikeUUID(organizationID) {
		return nil, nil
	}
	rows, err := s.q.Query(ctx, `
		SELECT id::text, organization_id::text, subject_identity_id::text, scope_type, scope_id::text, role, source
		FROM app.role_bindings
		WHERE subject_identity_id = $1::uuid AND organization_id = $2::uuid
		ORDER BY id`, subjectID, organizationID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Binding, error) {
		var b Binding
		var scope, role, source string
		err := r.Scan(&b.ID, &b.OrganizationID, &b.SubjectID, &scope, &b.ScopeID, &role, &source)
		b.ScopeType, b.Role, b.Source = ScopeType(scope), Role(role), Source(source)
		return b, err
	})
}

// OrganizationsForSubject implements BindingStore.
func (s *PGStore) OrganizationsForSubject(ctx context.Context, subjectID string) ([]string, error) {
	if !looksLikeUUID(subjectID) {
		return nil, nil
	}
	rows, err := s.q.Query(ctx, `
		SELECT DISTINCT organization_id::text FROM app.role_bindings
		WHERE subject_identity_id = $1::uuid ORDER BY 1`, subjectID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// GroupsForBusinessEntity implements BindingStore.
func (s *PGStore) GroupsForBusinessEntity(ctx context.Context, organizationID, businessEntityID string) ([]string, error) {
	if !looksLikeUUID(organizationID) || !looksLikeUUID(businessEntityID) {
		return nil, nil
	}
	rows, err := s.q.Query(ctx, `
		SELECT group_id::text FROM app.group_business_entities
		WHERE organization_id = $1::uuid AND business_entity_id = $2::uuid ORDER BY 1`, organizationID, businessEntityID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// ResourceOrganization implements BindingStore. Rows hidden by row-level
// security are reported as not found.
func (s *PGStore) ResourceOrganization(ctx context.Context, r Resource) (string, bool, error) {
	var query string
	switch r.Type {
	case ScopeOrganization:
		query = `SELECT id::text FROM app.organizations WHERE id = $1::uuid`
	case ScopeGroup:
		query = `SELECT organization_id::text FROM app.groups WHERE id = $1::uuid`
	case ScopeBusinessEntity:
		query = `SELECT organization_id::text FROM app.business_entities WHERE id = $1::uuid`
	default:
		return "", false, nil
	}
	if !looksLikeUUID(r.ID) {
		return "", false, nil // malformed identifiers are simply unknown
	}
	var org string
	err := s.q.QueryRow(ctx, query, r.ID).Scan(&org)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return org, true, nil
}

// OrganizationPolicy implements BindingStore. A missing (or invisible) row
// yields the documented default.
func (s *PGStore) OrganizationPolicy(ctx context.Context, organizationID string) (OrganizationPolicy, error) {
	var p OrganizationPolicy
	err := s.q.QueryRow(ctx, `
		SELECT group_admins_edit_business_profiles FROM app.organization_policies
		WHERE organization_id = $1::uuid`, organizationID).Scan(&p.GroupAdminsEditBusinessProfiles)
	if errors.Is(err, pgx.ErrNoRows) {
		return OrganizationPolicy{GroupAdminsEditBusinessProfiles: true}, nil
	}
	return p, err
}

// GroupsInOrganization implements GroupDirectory.
func (s *PGStore) GroupsInOrganization(ctx context.Context, organizationID string) ([]GroupInfo, error) {
	rows, err := s.q.Query(ctx, `
		SELECT id::text, organization_id::text, slug, name FROM app.groups
		WHERE organization_id = $1::uuid ORDER BY id`, organizationID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (GroupInfo, error) {
		var g GroupInfo
		err := r.Scan(&g.ID, &g.OrganizationID, &g.Slug, &g.Name)
		return g, err
	})
}

// looksLikeUUID accepts the canonical 8-4-4-4-12 hex form.
func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
	}
	return true
}

var (
	_ BindingStore   = (*PGStore)(nil)
	_ GroupDirectory = (*PGStore)(nil)
)
