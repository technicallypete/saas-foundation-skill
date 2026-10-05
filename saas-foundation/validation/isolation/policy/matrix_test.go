package policy_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"example.invalid/archetype/isolation/internal/fixture"
	"example.invalid/archetype/isolation/policy"
)

const (
	matrixBegin = "<!-- permission-matrix:begin -->"
	matrixEnd   = "<!-- permission-matrix:end -->"
	matrixDoc   = "tenancy-authorization.md"
)

// referencePath locates references/tenancy-authorization.md: the
// ARCHETYPE_REFERENCES_DIR override, else the skill layout relative to this
// package (validation/isolation/policy -> references). A missing document
// fails the test; doc-sync is never skipped.
func referencePath(t *testing.T) string {
	t.Helper()
	if dir := os.Getenv("ARCHETYPE_REFERENCES_DIR"); dir != "" {
		return filepath.Join(dir, matrixDoc)
	}
	p, err := filepath.Abs(filepath.Join("..", "..", "..", "references", matrixDoc))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("permission matrix reference not found at %s (mount the whole skill or set ARCHETYPE_REFERENCES_DIR): %v", p, err)
	}
	return p
}

// parseMatrix returns the header roles and capability -> role -> cell.
func parseMatrix(t *testing.T, doc string) ([]string, map[string]map[string]string, []string) {
	t.Helper()
	start := strings.Index(doc, matrixBegin)
	end := strings.Index(doc, matrixEnd)
	if start < 0 || end < start {
		t.Fatal("permission-matrix markers missing")
	}
	var header []string
	var order []string
	cells := map[string]map[string]string{}
	for _, line := range strings.Split(doc[start+len(matrixBegin):end], "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cols := strings.Split(strings.Trim(line, "|"), "|")
		for i := range cols {
			cols[i] = strings.TrimSpace(cols[i])
		}
		switch {
		case header == nil:
			header = cols[1:]
		case strings.HasPrefix(cols[0], "---"):
			continue
		default:
			if len(cols) != len(header)+1 {
				t.Fatalf("row %q has %d cells, header has %d", cols[0], len(cols)-1, len(header))
			}
			row := map[string]string{}
			for i, role := range header {
				row[role] = cols[i+1]
			}
			if _, dup := cells[cols[0]]; dup {
				t.Fatalf("duplicate matrix row %q", cols[0])
			}
			cells[cols[0]] = row
			order = append(order, cols[0])
		}
	}
	return header, cells, order
}

// Canonical scenario identifiers (in-memory only).
const (
	scOrg       = "matrix-org"
	scOwnGroup  = "matrix-group-own"
	scOtherGrp  = "matrix-group-other"
	scLinkedBE  = "matrix-be-linked"   // associated with the own Group; target of direct grants
	scUnlinkBE  = "matrix-be-unlinked" // associated with the other Group only
	scSubject   = "matrix-subject"
	scOtherUser = "matrix-other-subject"
)

// scenario builds one Organization where the subject holds exactly one
// binding with role r (at the own Group / linked BusinessEntity for Group
// and BusinessEntity roles) and the given Organization policy.
func scenario(t *testing.T, r policy.Role, policyOn bool) (*policy.Evaluator, policy.TenantContext) {
	t.Helper()
	s := policy.NewMemoryStore()
	s.AddIdentity(scSubject, "subject@example.test")
	s.AddIdentity(scOtherUser, "other@example.test")
	s.AddOrganization(scOrg, "matrix", "Matrix Org")
	s.AddGroup(scOwnGroup, scOrg, "own", "Own")
	s.AddGroup(scOtherGrp, scOrg, "other", "Other")
	s.AddBusinessEntity(scLinkedBE, scOrg, "Linked", "")
	s.AddBusinessEntity(scUnlinkBE, scOrg, "Unlinked", "")
	for _, a := range [][2]string{{scOwnGroup, scLinkedBE}, {scOtherGrp, scUnlinkBE}} {
		if err := s.Associate(scOrg, a[0], a[1]); err != nil {
			t.Fatal(err)
		}
	}
	// The other Group has its own member, which must never matter.
	if _, err := s.AddBinding(policy.Binding{OrganizationID: scOrg, SubjectID: scOtherUser, ScopeType: policy.ScopeGroup, ScopeID: scOtherGrp, Role: policy.RoleGroupAdmin, Source: policy.SourceMembership}); err != nil {
		t.Fatal(err)
	}
	scope, ok := policy.RoleScope(r)
	if !ok {
		t.Fatalf("unknown role %s", r)
	}
	b := policy.Binding{OrganizationID: scOrg, SubjectID: scSubject, ScopeType: scope, Role: r, Source: policy.SourceMembership}
	switch scope {
	case policy.ScopeOrganization:
		b.ScopeID = scOrg
	case policy.ScopeGroup:
		b.ScopeID = scOwnGroup
	case policy.ScopeBusinessEntity:
		b.ScopeID, b.Source = scLinkedBE, policy.SourceDirectGrant
	}
	if _, err := s.AddBinding(b); err != nil {
		t.Fatal(err)
	}
	s.SetOrganizationPolicy(scOrg, policy.OrganizationPolicy{GroupAdminsEditBusinessProfiles: policyOn})
	tc, err := policy.NewResolver(s, nil).Resolve(ctx, valid(scSubject), scOrg, "")
	if err != nil {
		t.Fatalf("resolve canonical subject for %s: %v", r, err)
	}
	return policy.NewEvaluator(s), tc
}

// cell renders the evaluator's decision for (role, capability) in the
// reference's cell vocabulary.
func cell(t *testing.T, r policy.Role, c policy.Capability) string {
	t.Helper()
	allowed := func(policyOn bool, res policy.Resource) bool {
		e, tc := scenario(t, r, policyOn)
		ok, err := e.Allowed(ctx, tc, c, res)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	name := string(c)
	switch {
	case strings.HasPrefix(name, "organization.") || strings.HasPrefix(name, "audit."):
		on, off := allowed(true, org(scOrg)), allowed(false, org(scOrg))
		if on != off {
			return "?policy-dependent"
		}
		return mark(on, "✓")
	case strings.HasPrefix(name, "group."):
		own, other := allowed(true, grp(scOwnGroup)), allowed(true, grp(scOtherGrp))
		switch {
		case own && other:
			return "✓ all"
		case own:
			return "✓ own"
		case other:
			return "?other-only"
		}
		return "—"
	case strings.HasPrefix(name, "business_entity."):
		on, off := allowed(true, be(scLinkedBE)), allowed(false, be(scLinkedBE))
		unlinked := allowed(true, be(scUnlinkBE))
		scope, _ := policy.RoleScope(r)
		if scope == policy.ScopeGroup {
			switch {
			case unlinked:
				return "?unlinked-allowed"
			case on && off:
				return "✓ via Group"
			case on && !off:
				return "✓ via Group if policy"
			case !on && off:
				return "?inverted-policy"
			}
			return "—"
		}
		if on != off {
			return "?policy-dependent"
		}
		if scope == policy.ScopeBusinessEntity && unlinked {
			return "?direct-grant-leaks"
		}
		if scope == policy.ScopeOrganization && unlinked != on {
			return "?org-role-partial"
		}
		return mark(on, "✓")
	}
	return "?unknown-capability"
}

func mark(ok bool, yes string) string {
	if ok {
		return yes
	}
	return "—"
}

func TestUnit_MatrixMatchesReference(t *testing.T) {
	fixture.Label(t, fixture.Unit)
	raw, err := os.ReadFile(referencePath(t))
	if err != nil {
		t.Fatal(err)
	}
	header, cells, order := parseMatrix(t, string(raw))

	var wantRoles []string
	for _, r := range policy.MatrixRoles {
		wantRoles = append(wantRoles, string(r))
	}
	if !slices.Equal(header, wantRoles) {
		t.Fatalf("matrix roles = %v, evaluator roles = %v", header, wantRoles)
	}
	var wantCaps []string
	for _, c := range policy.MatrixCapabilities {
		wantCaps = append(wantCaps, string(c))
	}
	if !slices.Equal(order, wantCaps) {
		t.Fatalf("matrix capabilities = %v, evaluator capabilities = %v", order, wantCaps)
	}
	compared := 0
	for _, c := range policy.MatrixCapabilities {
		for _, r := range policy.MatrixRoles {
			want := cells[string(c)][string(r)]
			got := cell(t, r, c)
			if got != want {
				t.Errorf("%s / %s: reference %q, evaluator %q", c, r, want, got)
			}
			compared++
		}
	}
	if compared != len(policy.MatrixCapabilities)*len(policy.MatrixRoles) {
		t.Fatalf("compared %d cells", compared)
	}
	t.Logf("compared %d matrix cells", compared)
}
