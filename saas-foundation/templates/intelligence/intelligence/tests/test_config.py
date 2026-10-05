"""I-CONFIG: capability configuration parsing and fail-fast validation."""

from __future__ import annotations

import pytest

from conftest import base_env
from main import create_app
from models import (
    Capability,
    CapabilityNotConfigured,
    ConfigError,
    ModelIdentity,
    ModelResolver,
    Settings,
)


def test_i_config_parses_all_capabilities_and_fallbacks() -> None:
    env = {
        "AI_MODEL_CHAT": "anthropic:test-chat",
        "AI_MODEL_CHAT_FALLBACKS": "openai:test-chat-b, google:test-chat-c",
        "AI_MODEL_FAST": "mistral:test-fast",
        "AI_MODEL_REASONING": "openai:test-reasoner",
        "AI_MODEL_REASONING_FALLBACKS": "xai:test-reasoner-b",
        "AI_MODEL_EXTRACTION": "meta:test-extractor:free",
        "AI_MODEL_EMBEDDING": "openai:test-embedding",
        "AI_MODEL_EMBEDDING_FALLBACKS": "",
    }
    resolver = ModelResolver.from_env(env)
    assert resolver.configured == tuple(Capability)
    chat = resolver.resolve(Capability.CHAT)
    assert chat.primary == ModelIdentity("anthropic", "test-chat")
    assert chat.fallbacks == (
        ModelIdentity("openai", "test-chat-b"),
        ModelIdentity("google", "test-chat-c"),
    )
    assert resolver.resolve(Capability.REASONING).fallbacks == (
        ModelIdentity("xai", "test-reasoner-b"),
    )
    assert resolver.resolve(Capability.EMBEDDING).fallbacks == ()


def test_i_config_splits_identity_on_first_colon() -> None:
    route = ModelResolver.from_env({"AI_MODEL_FAST": "meta:test-model:free"}).resolve(
        Capability.FAST
    )
    assert route.primary == ModelIdentity("meta", "test-model:free")


def test_i_config_zero_capabilities_is_valid() -> None:
    resolver = ModelResolver.from_env({})
    assert resolver.configured == ()
    assert Settings.from_env(base_env()).resolver.configured == ()


def test_i_config_empty_value_means_unset() -> None:
    resolver = ModelResolver.from_env({"AI_MODEL_CHAT": "", "AI_MODEL_CHAT_FALLBACKS": ""})
    assert not resolver.is_configured(Capability.CHAT)


@pytest.mark.parametrize(
    "value",
    [
        "no-colon-here",
        "anthropic:",
        ":test-model",
        "Anthropic:test-model",
        "1provider:test-model",
        "pro_vider:test-model",
        " anthropic:test-model",
        "anthropic:test model",
        "anthropic:test-model\t",
        "   ",
        "anthropic:author/test-model",
    ],
)
def test_i_config_rejects_malformed_identity(value: str) -> None:
    with pytest.raises(ConfigError) as exc:
        ModelResolver.from_env({"AI_MODEL_CHAT": value})
    assert any(p.startswith("AI_MODEL_CHAT:") for p in exc.value.problems)


@pytest.mark.parametrize("value", ["anthropic:ok,", "anthropic:ok,,openai:ok", "bad", "x:y z"])
def test_i_config_rejects_malformed_fallbacks(value: str) -> None:
    with pytest.raises(ConfigError) as exc:
        ModelResolver.from_env(
            {"AI_MODEL_CHAT": "openai:primary", "AI_MODEL_CHAT_FALLBACKS": value}
        )
    assert any(p.startswith("AI_MODEL_CHAT_FALLBACKS:") for p in exc.value.problems)


def test_i_config_rejects_fallbacks_without_primary() -> None:
    with pytest.raises(ConfigError) as exc:
        ModelResolver.from_env({"AI_MODEL_REASONING_FALLBACKS": "openai:test-reasoner"})
    assert (
        "AI_MODEL_REASONING_FALLBACKS: fallbacks require AI_MODEL_REASONING" in exc.value.problems
    )


@pytest.mark.parametrize(
    "fallbacks",
    ["openai:test-primary", "anthropic:test-b,anthropic:test-b"],
    ids=["equal-to-primary", "repeated"],
)
def test_i_config_rejects_duplicate_fallbacks(fallbacks: str) -> None:
    with pytest.raises(ConfigError) as exc:
        ModelResolver.from_env(
            {"AI_MODEL_CHAT": "openai:test-primary", "AI_MODEL_CHAT_FALLBACKS": fallbacks}
        )
    assert any("duplicates" in p for p in exc.value.problems)


@pytest.mark.parametrize(
    "name",
    ["AI_MODEL_CHATT", "AI_MODEL_CHATT_FALLBACKS", "AI_MODEL_", "AI_MODEL_chat", "AI_MODEL_VISION"],
)
def test_i_config_rejects_unknown_capability_names(name: str) -> None:
    with pytest.raises(ConfigError) as exc:
        ModelResolver.from_env({name: "openai:test-model"})
    assert any(p.startswith(f"{name}: unknown capability") for p in exc.value.problems)


@pytest.mark.parametrize(
    "env",
    [
        {"AI_MODEL_CHAT": "openrouter:anthropic-test-model"},
        {"AI_MODEL_CHAT": "openai:test-model", "AI_MODEL_CHAT_FALLBACKS": "openrouter:auto"},
    ],
    ids=["primary", "fallback"],
)
def test_i_config_rejects_gateway_provider(env: dict[str, str]) -> None:
    with pytest.raises(ConfigError) as exc:
        ModelResolver.from_env(env)
    assert any("gateway" in p for p in exc.value.problems)


def test_i_config_unconfigured_capability_raises_typed_error() -> None:
    resolver = ModelResolver.from_env({"AI_MODEL_CHAT": "openai:test-model"})
    with pytest.raises(CapabilityNotConfigured) as exc:
        resolver.resolve(Capability.EMBEDDING)
    assert exc.value.capability is Capability.EMBEDDING


def test_i_config_collects_all_problems_without_echoing_values() -> None:
    secretish = "sk-synthetic-value-that-must-not-echo"
    with pytest.raises(ConfigError) as exc:
        ModelResolver.from_env(
            {
                "AI_MODEL_CHAT": secretish,
                "AI_MODEL_FASTT": "openai:test",
                "AI_MODEL_REASONING_FALLBACKS": "openai:test",
            }
        )
    assert len(exc.value.problems) == 3
    assert secretish not in str(exc.value)


def test_i_config_ignores_gateway_default_variable() -> None:
    # AI_GATEWAY_DEFAULT is not part of the contract and is never read.
    resolver = ModelResolver.from_env({"AI_GATEWAY_DEFAULT": "not-a-valid:value with spaces"})
    assert resolver.configured == ()


@pytest.mark.parametrize(
    "env",
    [
        {"INTELLIGENCE_SERVICE_TOKEN": "x"},
        {"APP_ENV": "staging"},
        {"APP_ENV": "test", "AI_MODEL_CHAT": "openrouter:test"},
    ],
    ids=["missing-app-env", "bad-app-env", "bad-model"],
)
def test_i_config_app_startup_fails_fast(env: dict[str, str]) -> None:
    with pytest.raises(ConfigError):
        create_app(env)


def test_i_config_settings_repr_hides_secrets() -> None:
    settings = Settings.from_env(base_env(OPENROUTER_API_KEY="sk-or-synthetic-hidden"))
    assert "sk-or-synthetic-hidden" not in repr(settings)
    assert "synthetic-service-token" not in repr(settings)
