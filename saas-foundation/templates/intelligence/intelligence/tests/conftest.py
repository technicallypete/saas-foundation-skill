"""Shared fixtures: synthetic environment, deterministic stub transport and clients.

The stub transport is injected only through FastAPI dependency overrides; the
application itself has no switch that selects it. Nothing here touches the
network or needs a real API key.
"""

from __future__ import annotations

import copy
from collections.abc import Callable, Mapping
from datetime import UTC, datetime, timedelta
from typing import Any

import pytest
from fastapi.testclient import TestClient

from main import create_app, get_gateway, get_observability
from observability import ObservabilityHook
from providers import OpenRouterGateway

SERVICE_TOKEN = "synthetic-service-token-for-tests-0001"
REQUEST_ID = "req-0123456789abcdef"
TRACE_ID = "4bf92f3577b34da6a3ce929d0e0e4736"

CANNED_RESPONSE: dict[str, Any] = {
    "id": "gen-synthetic-1",
    "model": "anthropic/test-model-served",
    "choices": [{"index": 0, "message": {"role": "assistant", "content": "stub completion"}}],
    "usage": {"prompt_tokens": 12, "completion_tokens": 5, "cost": 0.00042},
}


class StubTransport:
    """Deterministic transport that records calls and returns a canned body."""

    def __init__(self, response: Mapping[str, Any] | None = None, error: Exception | None = None):
        self.response = copy.deepcopy(dict(response or CANNED_RESPONSE))
        self.error = error
        self.calls: list[dict[str, Any]] = []

    async def post_json(
        self, path: str, payload: Mapping[str, Any], timeout_s: float
    ) -> Mapping[str, Any]:
        self.calls.append(
            {"path": path, "payload": copy.deepcopy(dict(payload)), "timeout_s": timeout_s}
        )
        if self.error is not None:
            raise self.error
        return copy.deepcopy(self.response)


def base_env(**overrides: str) -> dict[str, str]:
    env = {"APP_ENV": "test", "INTELLIGENCE_SERVICE_TOKEN": SERVICE_TOKEN}
    env.update(overrides)
    return env


def make_envelope(**overrides: Any) -> dict[str, Any]:
    deadline = (datetime.now(UTC) + timedelta(minutes=5)).isoformat().replace("+00:00", "Z")
    envelope: dict[str, Any] = {
        "request_id": REQUEST_ID,
        "trace_id": TRACE_ID,
        "capability": "chat",
        "authorization_ref": "authz-ref-synthetic-1",
        "organization_id": "0b7c1a52-9a43-4c6e-9f0e-6f1f2d3c4b5a",
        "user_id": "5d2e8f61-3b7a-4c9d-8e1f-0a2b3c4d5e6f",
        "allowed_tools": [{"name": "records.search", "level": "read"}],
        "deadline": deadline,
        "context_refs": [{"kind": "document", "ref": "doc-synthetic-1"}],
        "input": {"messages": [{"role": "user", "content": "synthetic prompt"}]},
    }
    envelope.update(overrides)
    return envelope


def auth_headers(token: str = SERVICE_TOKEN) -> dict[str, str]:
    return {"Authorization": f"Bearer {token}"}


ClientFactory = Callable[..., TestClient]


@pytest.fixture
def stub_transport() -> StubTransport:
    return StubTransport()


@pytest.fixture
def make_client() -> ClientFactory:
    """Build a TestClient for an explicit environment, overriding dependencies."""

    def factory(
        env: Mapping[str, str] | None = None,
        transport: StubTransport | None = None,
        hook: ObservabilityHook | None = None,
    ) -> TestClient:
        app = create_app(base_env() if env is None else env)
        if transport is not None:
            gateway = OpenRouterGateway(transport)
            app.dependency_overrides[get_gateway] = lambda: gateway
        if hook is not None:
            app.dependency_overrides[get_observability] = lambda: hook
        return TestClient(app)

    return factory
