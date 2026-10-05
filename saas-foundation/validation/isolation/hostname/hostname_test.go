package hostname_test

import (
	"context"
	"os"
	"testing"
	"time"

	"example.invalid/archetype/isolation/hostname"
	"example.invalid/archetype/isolation/internal/fixture"
	"example.invalid/archetype/isolation/policy"
)

// The base domain is configuration; tests supply it through the same
// environment key the generated project uses.
const testBaseDomain = "Platform.Example.Test."

func newResolver(t *testing.T, s *policy.MemoryStore) *hostname.Resolver {
	t.Helper()
	t.Setenv(hostname.BaseDomainEnv, testBaseDomain)
	base, err := hostname.BaseDomainFromEnv(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if base != "platform.example.test" {
		t.Fatalf("normalized base domain = %q", base)
	}
	r, err := hostname.New(hostname.Config{
		BaseDomain: base,
		Directory:  s,
		CustomDomains: []hostname.CustomDomain{
			{Host: "Portal.Alpha-Customer.Example.", OrganizationID: fixture.OrgAlpha, Verified: true},
			{Host: "portal.bravo-customer.example", OrganizationID: fixture.OrgBravo, Verified: false},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestUnit_HostnameNormalizationAndSelection(t *testing.T) {
	fixture.Label(t, fixture.Unit)
	r := newResolver(t, policy.NewSeededMemoryStore())
	resolves := map[string]string{
		"alpha.platform.example.test":       fixture.OrgAlpha,
		"ALPHA.Platform.Example.Test":       fixture.OrgAlpha,
		"alpha.platform.example.test.":      fixture.OrgAlpha,
		"alpha.platform.example.test:8443":  fixture.OrgAlpha,
		"Alpha.Platform.Example.Test.:443":  fixture.OrgAlpha,
		"bravo.platform.example.test":       fixture.OrgBravo,
		"portal.alpha-customer.example":     fixture.OrgAlpha, // verified custom domain
		"PORTAL.alpha-customer.example.:80": fixture.OrgAlpha,
	}
	for host, want := range resolves {
		sel, ok := r.Resolve(host)
		if !ok || sel.OrganizationID != want {
			t.Errorf("%q resolved to %+v (%t), want %s", host, sel, ok, want)
		}
	}
	nothing := []string{
		"",
		"platform.example.test",                  // bare base domain
		"www.platform.example.test",              // not an Organization
		"charlie.platform.example.test",          // unknown slug
		"evilalpha.platform.example.test",        // look-alike slug
		"alpha-.platform.example.test",           // invalid label
		"alpha.platform.example.test.evil.test",  // base domain as a prefix of another domain
		"alpha.platform.example.test-evil.test",  // suffix confusion
		"alphaplatform.example.test",             // missing label boundary
		"x.alpha.platform.example.test",          // nested subdomain
		"ga-one.alpha.platform.example.test",     // Group slug as subdomain
		"portal.bravo-customer.example",          // unverified custom domain
		"sub.portal.alpha-customer.example",      // subdomain of a custom domain
		"alpha.platform.example.test:notaport:1", // malformed port
		"127.0.0.1",                              // IP literal
		"[::1]:8080",                             // IPv6 literal
		"alpha_platform.example.test",            // invalid character
		"alpha..platform.example.test",           // empty label
		"alpha.platform.example.test/../bravo",   // path smuggling
		"alpha.platform.example.test@bravo",      // userinfo smuggling
	}
	for _, host := range nothing {
		if sel, ok := r.Resolve(host); ok {
			t.Errorf("%q resolved to %+v, want nothing", host, sel)
		}
	}
	if _, err := hostname.New(hostname.Config{BaseDomain: "", Directory: policy.NewSeededMemoryStore()}); err == nil {
		t.Error("empty base domain accepted")
	}
}

func TestUnit_HostnameSelectorGrantsNothing(t *testing.T) {
	fixture.Label(t, fixture.Unit)
	s := policy.NewSeededMemoryStore()
	r := newResolver(t, s)
	ctx := context.Background()
	res := policy.NewResolver(s, nil)
	e := policy.NewEvaluator(s)
	p := func(id string) policy.Principal {
		return policy.Principal{IdentityID: id, ExpiresAt: time.Now().Add(time.Hour)}
	}

	bravo, ok := r.Resolve("bravo.platform.example.test")
	if !ok {
		t.Fatal("bravo host did not resolve")
	}
	// Positive control: ada holds a bravo binding, so the selector plus
	// her membership yields a context — with only org_member authority.
	tc, err := res.Resolve(ctx, p(fixture.IdentityAda), bravo.OrganizationID, "")
	if err != nil {
		t.Fatalf("ada on bravo host: %v", err)
	}
	if ok, _ := e.Allowed(ctx, tc, policy.CapGroupRead, policy.Resource{Type: policy.ScopeGroup, ID: fixture.GroupGB1}); ok {
		t.Error("bravo host gave ada (org_member) group access")
	}
	// lin, sam and kai have no bravo binding: the resolved Organization
	// grants them nothing, exactly like a nonexistent Organization.
	for _, id := range []string{fixture.IdentityLin, fixture.IdentitySam, fixture.IdentityKai} {
		if _, err := res.Resolve(ctx, p(id), bravo.OrganizationID, ""); err != policy.ErrNotFound {
			t.Errorf("%s on bravo host: %v, want ErrNotFound", id, err)
		}
	}
	// The verified custom domain is still only a selector.
	custom, ok := r.Resolve("portal.alpha-customer.example")
	if !ok {
		t.Fatal("custom domain did not resolve")
	}
	if _, err := res.Resolve(ctx, p(fixture.IdentityLin), custom.OrganizationID, ""); err != nil {
		t.Errorf("lin (GA1 grant) on alpha custom domain: %v", err)
	}

	// A foreign Group's slug grants nothing: on the alpha host, "gb-one"
	// is not an alpha Group, and its ID is rejected like an unknown one.
	alpha, _ := r.Resolve("alpha.platform.example.test")
	if _, found := s.GroupIDBySlug(alpha.OrganizationID, "gb-one"); found {
		t.Fatal("bravo Group slug resolved inside alpha")
	}
	if id, found := s.GroupIDBySlug(alpha.OrganizationID, "ga-one"); !found || id != fixture.GroupGA1 {
		t.Fatalf("control: ga-one in alpha = %q %t", id, found)
	}
	if _, err := res.Resolve(ctx, p(fixture.IdentityAda), alpha.OrganizationID, fixture.GroupGB1); err != policy.ErrNotFound {
		t.Errorf("alpha owner selecting bravo Group: %v, want ErrNotFound", err)
	}
	if _, err := res.Resolve(ctx, p(fixture.IdentityAda), alpha.OrganizationID, fixture.GroupGA1); err != nil {
		t.Errorf("control: alpha owner selecting GA1: %v", err)
	}
}
