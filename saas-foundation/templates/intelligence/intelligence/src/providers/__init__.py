"""Model gateway boundary and adapters."""

from providers.gateway import (
    CompletionRequest,
    CompletionResult,
    GatewayError,
    Message,
    ModelGateway,
    UsageRecord,
)
from providers.openrouter import (
    PROVIDER_ALIASES,
    HttpxTransport,
    OpenRouterGateway,
    Transport,
    to_openrouter_id,
)

__all__ = [
    "PROVIDER_ALIASES",
    "CompletionRequest",
    "CompletionResult",
    "GatewayError",
    "HttpxTransport",
    "Message",
    "ModelGateway",
    "OpenRouterGateway",
    "Transport",
    "UsageRecord",
    "to_openrouter_id",
]
