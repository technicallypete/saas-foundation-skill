"""Capability-to-model resolution from ``AI_MODEL_<CAPABILITY>`` configuration.

A capability is enabled exactly when ``AI_MODEL_<CAP>`` is non-empty. Each
value is a canonical ``provider:model`` identity, split on the FIRST colon so
the model part may carry further colons (for example routing variant
suffixes). ``AI_MODEL_<CAP>_FALLBACKS`` is an optional comma-separated list of
identities tried in order after the primary.

Gateways (such as OpenRouter) are transport, not providers: naming a gateway
as the provider is rejected so the gateway never becomes a domain concept.
"""

from __future__ import annotations

import re
from collections.abc import Mapping
from dataclasses import dataclass
from enum import StrEnum

ENV_PREFIX = "AI_MODEL_"
FALLBACKS_SUFFIX = "_FALLBACKS"

# Gateway names that must never appear as a provider in AI_MODEL_* values.
GATEWAY_NAMES = frozenset({"openrouter"})

_PROVIDER_RE = re.compile(r"^[a-z][a-z0-9-]*$")


class Capability(StrEnum):
    """Model capabilities the service can be configured for."""

    CHAT = "chat"
    FAST = "fast"
    REASONING = "reasoning"
    EXTRACTION = "extraction"
    EMBEDDING = "embedding"

    @property
    def env_name(self) -> str:
        return f"{ENV_PREFIX}{self.name}"


class ConfigError(ValueError):
    """Invalid model configuration. Messages name variables, never values."""

    def __init__(self, problems: list[str]) -> None:
        self.problems = problems
        super().__init__("invalid configuration: " + "; ".join(problems))


class CapabilityNotConfigured(LookupError):
    """Raised when resolving a capability with no configured model."""

    def __init__(self, capability: Capability) -> None:
        self.capability = capability
        super().__init__(f"capability {capability.value!r} is not configured")


@dataclass(frozen=True, slots=True)
class ModelIdentity:
    """A canonical ``provider:model`` identity."""

    provider: str
    model: str

    def __str__(self) -> str:
        return f"{self.provider}:{self.model}"


@dataclass(frozen=True, slots=True)
class ModelRoute:
    """Resolved route for one capability: a primary and ordered fallbacks."""

    capability: Capability
    primary: ModelIdentity
    fallbacks: tuple[ModelIdentity, ...] = ()


def parse_identity(value: str) -> ModelIdentity:
    """Parse ``provider:model``; raise ``ValueError`` with a value-free reason."""
    if value != value.strip() or any(ch.isspace() for ch in value):
        raise ValueError("must not contain whitespace")
    provider, sep, model = value.partition(":")
    if not sep:
        raise ValueError("must have the form provider:model")
    if not _PROVIDER_RE.match(provider):
        raise ValueError("provider must match ^[a-z][a-z0-9-]*$")
    if provider in GATEWAY_NAMES:
        raise ValueError("names a gateway as the provider; use the model's provider instead")
    if not model:
        raise ValueError("model must not be empty")
    if "/" in model:
        raise ValueError("model must be the provider's model name, without a '/' path")
    return ModelIdentity(provider=provider, model=model)


class ModelResolver:
    """Immutable capability routes parsed from the environment."""

    def __init__(self, routes: Mapping[Capability, ModelRoute]) -> None:
        self._routes = dict(routes)

    @classmethod
    def from_env(cls, env: Mapping[str, str]) -> ModelResolver:
        """Build a resolver, collecting every problem before failing."""
        problems: list[str] = []
        known = {cap.name: cap for cap in Capability}

        for key in sorted(env):
            if not key.startswith(ENV_PREFIX):
                continue
            name = key.removeprefix(ENV_PREFIX)
            name = name.removesuffix(FALLBACKS_SUFFIX) if name.endswith(FALLBACKS_SUFFIX) else name
            if name not in known:
                problems.append(f"{key}: unknown capability (expected one of {_cap_list()})")

        routes: dict[Capability, ModelRoute] = {}
        for cap in Capability:
            primary_var = cap.env_name
            fallbacks_var = primary_var + FALLBACKS_SUFFIX
            primary_raw = env.get(primary_var, "")
            fallbacks_raw = env.get(fallbacks_var, "")

            primary: ModelIdentity | None = None
            if primary_raw:
                try:
                    primary = parse_identity(primary_raw)
                except ValueError as exc:
                    problems.append(f"{primary_var}: {exc}")

            fallbacks: list[ModelIdentity] = []
            if fallbacks_raw:
                if not primary_raw:
                    problems.append(f"{fallbacks_var}: fallbacks require {primary_var}")
                seen = {str(primary)} if primary else set()
                for position, item in enumerate(fallbacks_raw.split(","), start=1):
                    item = item.strip()
                    if not item:
                        problems.append(f"{fallbacks_var}: entry {position} is empty")
                        continue
                    try:
                        identity = parse_identity(item)
                    except ValueError as exc:
                        problems.append(f"{fallbacks_var}: entry {position} {exc}")
                        continue
                    if str(identity) in seen:
                        problems.append(
                            f"{fallbacks_var}: entry {position} duplicates the primary "
                            "or an earlier fallback"
                        )
                        continue
                    seen.add(str(identity))
                    fallbacks.append(identity)

            if primary is not None:
                routes[cap] = ModelRoute(cap, primary, tuple(fallbacks))

        if problems:
            raise ConfigError(problems)
        return cls(routes)

    @property
    def configured(self) -> tuple[Capability, ...]:
        """Configured capabilities, in declaration order."""
        return tuple(cap for cap in Capability if cap in self._routes)

    def is_configured(self, capability: Capability) -> bool:
        return capability in self._routes

    def resolve(self, capability: Capability) -> ModelRoute:
        """Return the route for ``capability`` or raise ``CapabilityNotConfigured``."""
        try:
            return self._routes[capability]
        except KeyError:
            raise CapabilityNotConfigured(capability) from None


def _cap_list() -> str:
    return ", ".join(cap.name for cap in Capability)
