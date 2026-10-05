package mcp

import (
	"context"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"example.invalid/archetype/isolation/policy"
)

// Authenticator turns a bearer credential into an authenticated principal.
// Production authenticators belong to the identity stage; the fixture's
// dev-only authenticator exists only in test files.
type Authenticator func(ctx context.Context, bearer string) (policy.Principal, error)

// HTTPOptions configures the streamable-HTTP endpoint.
type HTTPOptions struct {
	Config       Config
	Authenticate Authenticator
	// SelectOrganization optionally maps a request to an Organization
	// selector (for example from the hostname). The selector grants
	// nothing: the resolver validates the principal's grants, and a failed
	// selection leaves the session without context.
	SelectOrganization func(*http.Request) (string, bool)
}

// NewHTTPHandler returns a stateless streamable-HTTP MCP endpoint. Every
// request is bearer-authenticated (anonymous requests get 401) and gets a
// Server built for that authenticated identity, so tools/list differs per
// identity and context.
func NewHTTPHandler(o HTTPOptions) http.Handler {
	verify := func(ctx context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		p, err := o.Authenticate(ctx, token)
		if err != nil || p.IdentityID == "" {
			return nil, fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
		}
		return &auth.TokenInfo{UserID: p.IdentityID, Expiration: p.ExpiresAt}, nil
	}
	getServer := func(r *http.Request) *sdk.Server {
		ti := auth.TokenInfoFromContext(r.Context())
		if ti == nil {
			return nil
		}
		s, err := NewSession(r.Context(), o.Config, policy.Principal{IdentityID: ti.UserID, ExpiresAt: ti.Expiration})
		if err != nil {
			return nil
		}
		if o.SelectOrganization != nil {
			if org, ok := o.SelectOrganization(r); ok {
				_ = s.SelectOrganization(r.Context(), org)
			}
		}
		return s.Server()
	}
	h := sdk.NewStreamableHTTPHandler(getServer, &sdk.StreamableHTTPOptions{Stateless: true})
	return auth.RequireBearerToken(verify, nil)(h)
}
