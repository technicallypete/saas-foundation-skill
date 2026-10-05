"""OpenRouter adapter for the ModelGateway boundary.

Canonical ``provider:model`` identities become OpenRouter ``author/slug`` IDs
through a provider alias table; anything after the first colon (including
variant suffixes such as ``:free``) is preserved. Fallbacks are sent as the
ordered ``models`` list. The HTTP transport is injectable so tests run against
a deterministic stub with no network and no API key.
"""

from __future__ import annotations

import time
from collections.abc import Callable, Mapping
from typing import Any, Protocol

import httpx

from models import ModelIdentity, ModelRoute
from providers.gateway import (
    CompletionRequest,
    CompletionResult,
    GatewayError,
    UsageRecord,
)

GATEWAY_NAME = "openrouter"
DEFAULT_BASE_URL = "https://openrouter.ai/api/v1"
CHAT_COMPLETIONS_PATH = "/chat/completions"
UNKNOWN = "unknown"

# Canonical provider name -> OpenRouter author slug, where they differ.
PROVIDER_ALIASES: Mapping[str, str] = {
    "meta": "meta-llama",
    "mistral": "mistralai",
    "xai": "x-ai",
}


def to_openrouter_id(identity: ModelIdentity) -> str:
    """Translate ``provider:model`` into OpenRouter's ``author/slug[:variant]``."""
    author = PROVIDER_ALIASES.get(identity.provider, identity.provider)
    return f"{author}/{identity.model}"


class Transport(Protocol):
    """Sends one JSON request to the gateway and returns the decoded body."""

    async def post_json(
        self, path: str, payload: Mapping[str, Any], timeout_s: float
    ) -> Mapping[str, Any]: ...


class HttpxTransport:
    """Production transport. Refuses to send without an API key."""

    def __init__(self, api_key: str, base_url: str = DEFAULT_BASE_URL) -> None:
        self._api_key = api_key
        self._base_url = base_url.rstrip("/")

    def __repr__(self) -> str:
        return f"HttpxTransport(base_url={self._base_url!r})"

    async def post_json(
        self, path: str, payload: Mapping[str, Any], timeout_s: float
    ) -> Mapping[str, Any]:
        if not self._api_key:
            raise GatewayError(
                "gateway_not_configured", "the model gateway has no API key", retryable=False
            )
        headers = {"Authorization": f"Bearer {self._api_key}"}
        try:
            async with httpx.AsyncClient(timeout=timeout_s) as client:
                response = await client.post(self._base_url + path, json=payload, headers=headers)
        except httpx.TimeoutException:
            raise GatewayError(
                "gateway_timeout", "the model gateway timed out", retryable=True
            ) from None
        except httpx.HTTPError:
            raise GatewayError(
                "gateway_error", "the model gateway was unreachable", retryable=True
            ) from None
        if response.status_code >= 400:
            # The response body is not echoed: it may contain request content.
            retryable = response.status_code == 429 or response.status_code >= 500
            raise GatewayError(
                "gateway_error",
                f"the model gateway returned HTTP {response.status_code}",
                retryable=retryable,
            )
        try:
            body = response.json()
        except ValueError:
            raise GatewayError(
                "gateway_error", "the model gateway returned non-JSON", retryable=True
            ) from None
        if not isinstance(body, dict):
            raise GatewayError("gateway_error", "unexpected gateway response", retryable=False)
        return body


class OpenRouterGateway:
    """ModelGateway implementation for OpenRouter's chat completions API."""

    name = GATEWAY_NAME

    def __init__(self, transport: Transport, clock: Callable[[], float] = time.monotonic) -> None:
        self._transport = transport
        self._clock = clock

    @staticmethod
    def build_payload(request: CompletionRequest) -> dict[str, Any]:
        route: ModelRoute = request.route
        primary = to_openrouter_id(route.primary)
        payload: dict[str, Any] = {
            "model": primary,
            "messages": [{"role": m.role, "content": m.content} for m in request.messages],
            "usage": {"include": True},
        }
        if route.fallbacks:
            payload["models"] = [primary, *(to_openrouter_id(f) for f in route.fallbacks)]
        return payload

    async def complete(self, request: CompletionRequest) -> CompletionResult:
        payload = self.build_payload(request)
        started = self._clock()
        body = await self._transport.post_json(CHAT_COMPLETIONS_PATH, payload, request.timeout_s)
        latency_ms = max(0, int((self._clock() - started) * 1000))
        return CompletionResult(
            text=_extract_text(body),
            usage=_normalize_usage(request.route.primary, body, latency_ms),
        )


def _extract_text(body: Mapping[str, Any]) -> str:
    try:
        content = body["choices"][0]["message"]["content"]
    except (KeyError, IndexError, TypeError):
        raise GatewayError(
            "gateway_error", "gateway response had no completion", retryable=False
        ) from None
    if not isinstance(content, str):
        raise GatewayError("gateway_error", "gateway completion was not text", retryable=False)
    return content


def _non_empty_str(value: Any) -> str | None:
    return value if isinstance(value, str) and value else None


def _count(value: Any) -> int | None:
    return value if isinstance(value, int) and not isinstance(value, bool) and value >= 0 else None


def _normalize_usage(
    configured: ModelIdentity, body: Mapping[str, Any], latency_ms: int
) -> UsageRecord:
    usage = body.get("usage")
    usage = usage if isinstance(usage, Mapping) else {}
    cost = usage.get("cost")
    cost_ok = isinstance(cost, int | float) and not isinstance(cost, bool) and cost >= 0
    return UsageRecord(
        configured_provider=configured.provider,
        configured_model=configured.model,
        gateway=GATEWAY_NAME,
        # The served provider is taken only from router metadata; it is never
        # inferred from the model name.
        actual_provider=_non_empty_str(body.get("provider")) or UNKNOWN,
        actual_model=_non_empty_str(body.get("model")) or UNKNOWN,
        input_tokens=_count(usage.get("prompt_tokens")),
        output_tokens=_count(usage.get("completion_tokens")),
        latency_ms=latency_ms,
        estimated_cost=float(cost) if cost_ok else None,
    )
