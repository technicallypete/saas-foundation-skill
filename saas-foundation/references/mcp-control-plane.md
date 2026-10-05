# MCP control plane

## Control-plane tools

`account.get_current_user`, `account.list_organizations`, `account.get_current_context`, `account.switch_organization`, `context.list_groups`, `context.switch_group`, `logout`. They are the small surface available to every authenticated session.

## Switching flow

authenticate → switch Organization → validate the Organization binding → optionally switch Group → validate the Group membership or effective grant → resolve effective capabilities → recalculate the session's tool catalog → `tools/list` shows only what the active context allows.

- Switching Organization clears the Group scope and any Organization-specific resource context.
- Ordinary tool arguments (`organization_id`, `group_id`, …) can never change the active context; only the switching tools can, and only after validation.
- Membership, role or policy changes and session expiry invalidate the catalog. Sessions carry a policy/version marker; a changed marker forces recalculation on the next request.

## Discovery is not authorization

Every execution independently revalidates identity, active Organization and Group, current bindings, resource ownership, the required capability and any approval policy. A hidden tool's direct call fails exactly like an unknown tool. A tool still listed in a stale catalog fails at execution with a policy denial (the fixture proves this by suspending catalog reconciliation and revoking, downgrading, changing policy and expiring the session). Denials and switches are audited.

## Official MCP Go SDK notes (v1.8.x)

- `tools/list` returns every tool registered on a `*mcp.Server`; there is no per-caller filter. Use **one server per authenticated session context** and reconcile its tools with `AddTool`/`RemoveTools`, which also emits `notifications/tools/list_changed`.
- Over HTTP, mount `mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{Stateless: true})`, where `getServer` builds the server from the **authenticated** request (bearer token or session) — never from client-supplied tenant identifiers. Unauthenticated requests receive 401.
- The SDK only logs invalid tool names; assert names match `^[A-Za-z0-9_.-]{1,128}$` in tests.
- Add the MCP mount together with authentication (STAGE-IDENTITY, STAGE-MCP); never expose an unauthenticated endpoint.
