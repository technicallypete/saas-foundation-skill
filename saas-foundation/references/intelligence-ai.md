# Optional intelligence and AI configuration

## Ownership

Core owns tenant and policy checks, public streams, cancellation and deadlines, approvals, audit and durable usage attribution. Intelligence receives a core-authorized **execution envelope**, runs inference, and emits provider-independent **typed events**. The browser and `web` never call intelligence; Compose keeps intelligence on the `inference` network with core only, and it holds no data-store credentials.

## Model configuration

```text
AI_MODEL_<CAPABILITY>=<provider>:<model>
AI_MODEL_<CAPABILITY>_FALLBACKS=<provider>:<model>,<provider>:<model>
```

- Capabilities: `CHAT`, `FAST`, `REASONING`, `EXTRACTION`, `EMBEDDING`. A capability is enabled exactly when its variable is set; requesting an unconfigured capability yields a typed `CapabilityNotConfigured` error (`capability_not_configured` on the wire).
- The identity is split on the **first** colon, so gateway variant suffixes stay in the model part.
- The provider is the **model provider**, never the gateway (`openrouter:` is rejected). Unknown capability names, malformed identities, duplicate fallbacks and fallbacks without a primary fail startup.
- Never create per-feature model variables; never require `AI_GATEWAY_DEFAULT`. Sample identities in `.env.example` are commented samples, not availability claims.
- Intelligence disabled ⇒ no Python service, no AI variables, no credentials, no live calls.

## Resolver → Gateway → adapter

Application capability → `ModelResolver` → `ModelGateway` → OpenRouter adapter (default). The adapter translates `provider:model` to the gateway's `author/slug` identifiers (with a provider alias table), sends fallbacks in the gateway's native form, and normalizes responses. `OPENROUTER_API_KEY` is a secret provider credential. Direct-provider adapters are extension points; embeddings may need one (STAGE-AI).

Usage records distinguish the configured provider and model from the gateway and the **actual** provider and model. The actual model comes from the response; the actual provider is recorded only when the gateway reports it, otherwise `unknown` (never inferred).

## Contracts (`contracts/intelligence/`)

- `execution-envelope.schema.json`: request and trace IDs, capability, opaque authorization reference, Organization, optional Group and BusinessEntity, user, allowed tools with a read/propose/commit level, deadline, pre-filtered context references, optional callback grant reference, input. No credentials; `additionalProperties: false`.
- `stream-event.schema.json`: `text_delta`, `tool_call`, `tool_result`, `citation`, `structured_output`, `usage`, `error`, `done`. Every event carries `request_id`. `usage` carries capability, configured and actual identities, gateway, tokens or units, latency, estimated cost and trace — **never tenant or user dimensions**.

## Rules for later stages (STAGE-AI)

- **Durable usage** is built by core: join each `usage` event to core's own envelope by `request_id`; user, Organization, Group and BusinessEntity come from the envelope. Observability vendors are never the ledger.
- **Authorized callbacks** present the envelope's callback grant; core reauthorizes against current grants and ignores tenant identifiers supplied by intelligence.
- **Retrieval** is filtered by authorization before anything reaches a model; memory and context are scoped to the envelope; no cross-tenant memory.
- **Envelope integrity:** replace the skeleton's static service token with core-signed, short-lived, audience-bound envelopes.
- **Observability privacy:** default exports and logs exclude prompts, completions, retrieved content and secrets; content capture is an explicit, reviewed opt-in.
