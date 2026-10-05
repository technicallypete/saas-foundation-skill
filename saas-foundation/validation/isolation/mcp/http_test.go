package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"example.invalid/archetype/isolation/hostname"
	"example.invalid/archetype/isolation/internal/fixture"
	"example.invalid/archetype/isolation/policy"
)

// devBearerTokens is the dev-only fixture credential table (test files only;
// never rendered). Tokens are synthetic.
var devBearerTokens = map[string]string{
	"dev-token-ada": fixture.IdentityAda,
	"dev-token-lin": fixture.IdentityLin,
}

func devBearerAuthenticator(_ context.Context, bearer string) (policy.Principal, error) {
	id, ok := devBearerTokens[bearer]
	if !ok {
		return policy.Principal{}, errors.New("unknown token")
	}
	return policy.Principal{IdentityID: id, ExpiresAt: time.Now().Add(time.Hour)}, nil
}

// fixtureHostHeader carries the virtual host in this test. The SDK rejects a
// non-localhost Host header on a loopback listener (DNS-rebinding
// protection, kept enabled), so the test-supplied selector reads the
// virtual host from this header instead. Selectors grant nothing either way.
const fixtureHostHeader = "X-Fixture-Host"

// hostTransport sets the bearer credential and the virtual host of every
// request.
type hostTransport struct {
	bearer, host string
}

func (h hostTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if h.bearer != "" {
		r.Header.Set("Authorization", "Bearer "+h.bearer)
	}
	r.Header.Set(fixtureHostHeader, h.host)
	return http.DefaultTransport.RoundTrip(r)
}

func httpTools(t *testing.T, url, bearer, host string) []string {
	t.Helper()
	tr := &sdk.StreamableClientTransport{
		Endpoint:             url,
		HTTPClient:           &http.Client{Transport: hostTransport{bearer: bearer, host: host}},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "fixture-http-client", Version: "0.0.0"}, nil).Connect(context.Background(), tr, nil)
	if err != nil {
		t.Fatalf("connect as %s on %s: %v", bearer, host, err)
	}
	defer cs.Close()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list as %s: %v", bearer, err)
	}
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	slices.Sort(names)
	return names
}

func TestUnit_MCPStreamableHTTPPerIdentity(t *testing.T) {
	fixture.Label(t, fixture.RealSDKHTTP)
	store := policy.NewSeededMemoryStore()
	hosts, err := hostname.New(hostname.Config{BaseDomain: "platform.example.test", Directory: store})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHTTPHandler(HTTPOptions{
		Config:       Config{Store: store, Audit: &MemoryAuditSink{}, Approvals: &MemoryApprovals{}},
		Authenticate: devBearerAuthenticator,
		SelectOrganization: func(r *http.Request) (string, bool) {
			sel, ok := hosts.Resolve(r.Header.Get(fixtureHostHeader))
			return sel.OrganizationID, ok
		},
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	alphaHost := "alpha.platform.example.test"
	ada := httpTools(t, srv.URL, "dev-token-ada", alphaHost)
	lin := httpTools(t, srv.URL, "dev-token-lin", alphaHost)
	assertTools(t, ada, catalog(accountTools, contextTools, []string{
		"business_entities.get", "business_entities.transfer_ownership", "business_entities.update",
		"groups.list", "groups.update", "members.manage"}))
	assertTools(t, lin, catalog(accountTools, contextTools, []string{
		"business_entities.get", "business_entities.update", "groups.list", "groups.update", "members.manage"}))
	if slices.Equal(ada, lin) {
		t.Fatal("two identities received the same tools/list")
	}
	// The bravo host selects bravo, which grants lin nothing.
	assertTools(t, httpTools(t, srv.URL, "dev-token-lin", "bravo.platform.example.test"), catalog(accountTools))

	// Anonymous and unknown credentials are rejected before any Server exists.
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	for name, auth := range map[string]string{"anonymous": "", "unknown token": "Bearer not-a-token", "wrong scheme": "Basic ZGV2OmRldg=="} {
		req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(fixtureHostHeader, alphaHost)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, resp.StatusCode)
		}
	}
}
