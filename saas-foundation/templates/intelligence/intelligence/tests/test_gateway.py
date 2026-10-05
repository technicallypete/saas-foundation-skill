"""I-GATEWAY: OpenRouter adapter through a deterministic stub transport."""

from __future__ import annotations

import asyncio
import os
from pathlib import Path

import pytest

from conftest import CANNED_RESPONSE, StubTransport, base_env
from main import create_app
from models import Capability, ModelIdentity, ModelRoute
from providers import (
    CompletionRequest,
    GatewayError,
    HttpxTransport,
    Message,
    OpenRouterGateway,
    to_openrouter_id,
)

SRC = Path(__file__).resolve().parents[1] / "src"


def _complete(transport: StubTransport, route: ModelRoute):
    request = CompletionRequest(route=route, messages=(Message("user", "hi"),), timeout_s=5.0)
    return asyncio.run(OpenRouterGateway(transport).complete(request))


@pytest.mark.parametrize(
    ("identity", "expected"),
    [
        (ModelIdentity("meta", "test-llama"), "meta-llama/test-llama"),
        (ModelIdentity("mistral", "test-mistral"), "mistralai/test-mistral"),
        (ModelIdentity("xai", "test-grok"), "x-ai/test-grok"),
        (ModelIdentity("anthropic", "test-model"), "anthropic/test-model"),
        (ModelIdentity("openai", "test-model-2025-01-01"), "openai/test-model-2025-01-01"),
    ],
)
def test_i_gateway_translates_identity_with_alias_table(
    identity: ModelIdentity, expected: str
) -> None:
    assert to_openrouter_id(identity) == expected


def test_i_gateway_preserves_variant_suffixes() -> None:
    assert (
        to_openrouter_id(ModelIdentity("meta", "test-llama:free")) == "meta-llama/test-llama:free"
    )
    assert to_openrouter_id(ModelIdentity("openai", "test:a:b")) == "openai/test:a:b"


def test_i_gateway_sends_primary_and_fallbacks_as_models_list() -> None:
    transport = StubTransport()
    route = ModelRoute(
        Capability.CHAT,
        ModelIdentity("anthropic", "test-primary"),
        (ModelIdentity("meta", "test-llama:free"), ModelIdentity("xai", "test-grok")),
    )
    _complete(transport, route)
    (call,) = transport.calls
    assert call["path"] == "/chat/completions"
    assert call["payload"]["model"] == "anthropic/test-primary"
    assert call["payload"]["models"] == [
        "anthropic/test-primary",
        "meta-llama/test-llama:free",
        "x-ai/test-grok",
    ]
    assert call["payload"]["messages"] == [{"role": "user", "content": "hi"}]


def test_i_gateway_omits_models_list_without_fallbacks() -> None:
    transport = StubTransport()
    _complete(transport, ModelRoute(Capability.FAST, ModelIdentity("openai", "test-fast")))
    assert "models" not in transport.calls[0]["payload"]


def test_i_gateway_records_actual_model_from_response() -> None:
    result = _complete(
        StubTransport(), ModelRoute(Capability.CHAT, ModelIdentity("anthropic", "test-model"))
    )
    usage = result.usage
    assert result.text == "stub completion"
    assert (usage.configured_provider, usage.configured_model) == ("anthropic", "test-model")
    assert usage.gateway == "openrouter"
    assert usage.actual_model == CANNED_RESPONSE["model"] != "anthropic/test-model"
    assert (usage.input_tokens, usage.output_tokens) == (12, 5)
    assert usage.estimated_cost == pytest.approx(0.00042)
    assert usage.latency_ms >= 0


def test_i_gateway_actual_provider_unknown_without_router_metadata() -> None:
    result = _complete(
        StubTransport(), ModelRoute(Capability.CHAT, ModelIdentity("anthropic", "m"))
    )
    assert result.usage.actual_provider == "unknown"


def test_i_gateway_actual_provider_from_router_metadata() -> None:
    transport = StubTransport({**CANNED_RESPONSE, "provider": "SyntheticHost"})
    result = _complete(transport, ModelRoute(Capability.CHAT, ModelIdentity("anthropic", "m")))
    assert result.usage.actual_provider == "SyntheticHost"


def test_i_gateway_missing_usage_fields_are_null() -> None:
    transport = StubTransport({"model": "a/b", "choices": [{"message": {"content": "x"}}]})
    usage = _complete(transport, ModelRoute(Capability.CHAT, ModelIdentity("anthropic", "m"))).usage
    assert (usage.input_tokens, usage.output_tokens, usage.estimated_cost) == (None, None, None)


def test_i_gateway_malformed_response_raises_gateway_error() -> None:
    with pytest.raises(GatewayError):
        _complete(
            StubTransport({"model": "a/b"}), ModelRoute(Capability.CHAT, ModelIdentity("a", "m"))
        )


def test_i_gateway_no_api_key_and_no_gateway_default() -> None:
    assert not os.environ.get("OPENROUTER_API_KEY")
    # The contract has no gateway-default variable; no source file reads one.
    for path in SRC.rglob("*.py"):
        assert "AI_GATEWAY_DEFAULT" not in path.read_text(encoding="utf-8"), path


def test_i_gateway_http_transport_refuses_without_key() -> None:
    with pytest.raises(GatewayError) as exc:
        asyncio.run(HttpxTransport("").post_json("/chat/completions", {}, 1.0))
    assert exc.value.code == "gateway_not_configured"


def test_i_gateway_default_app_uses_real_adapter_without_stub_switch() -> None:
    app = create_app(base_env())
    gateway = app.state.gateway
    assert isinstance(gateway, OpenRouterGateway)
    assert isinstance(gateway._transport, HttpxTransport)
