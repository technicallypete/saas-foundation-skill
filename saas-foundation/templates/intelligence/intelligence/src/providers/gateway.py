"""ModelGateway boundary: the only way the service reaches a model.

Callers pass a resolved ``ModelRoute`` (canonical ``provider:model``
identities); each gateway adapter translates those into its own wire format
and normalizes the response into a ``UsageRecord`` that keeps configured and
actual identities apart.
"""

from __future__ import annotations

from collections.abc import Sequence
from dataclasses import dataclass
from typing import Protocol

from models import ModelRoute


@dataclass(frozen=True, slots=True)
class Message:
    role: str
    content: str


@dataclass(frozen=True, slots=True)
class CompletionRequest:
    route: ModelRoute
    messages: Sequence[Message]
    timeout_s: float


@dataclass(frozen=True, slots=True)
class UsageRecord:
    """Normalized, content-free usage for one gateway call."""

    configured_provider: str
    configured_model: str
    gateway: str
    actual_provider: str
    actual_model: str
    input_tokens: int | None
    output_tokens: int | None
    latency_ms: int
    estimated_cost: float | None


@dataclass(frozen=True, slots=True)
class CompletionResult:
    text: str
    usage: UsageRecord


class GatewayError(Exception):
    """A gateway call failed. ``code`` and the message never carry content."""

    def __init__(self, code: str, message: str, *, retryable: bool) -> None:
        self.code = code
        self.retryable = retryable
        super().__init__(message)


class ModelGateway(Protocol):
    """A model gateway adapter."""

    name: str

    async def complete(self, request: CompletionRequest) -> CompletionResult:
        """Run a text completion through the route's primary and fallbacks."""
        ...
