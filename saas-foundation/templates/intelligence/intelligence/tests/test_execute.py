"""I-EXECUTE: envelope validation and schema-valid NDJSON event streams."""

from __future__ import annotations

import json
from datetime import UTC, datetime, timedelta
from typing import Any

import pytest

from conftest import REQUEST_ID, TRACE_ID, StubTransport, auth_headers, base_env, make_envelope
from providers import GatewayError
from streaming import event_validator

EXECUTE = "/internal/v1/execute"
CHAT_ENV = base_env(AI_MODEL_CHAT="anthropic:test-model", AI_MODEL_CHAT_FALLBACKS="openai:test-b")


def _events(response) -> list[dict[str, Any]]:
    assert response.status_code == 200, response.text
    assert response.headers["content-type"].startswith("application/x-ndjson")
    lines = [line for line in response.text.split("\n") if line]
    events = [json.loads(line) for line in lines]
    validator = event_validator()
    for event in events:
        errors = list(validator.iter_errors(event))
        assert not errors, (event, [e.message for e in errors])
        assert event["request_id"] == REQUEST_ID
    return events


def test_i_execute_streams_text_usage_done(make_client) -> None:
    transport = StubTransport()
    client = make_client(env=CHAT_ENV, transport=transport)
    events = _events(client.post(EXECUTE, json=make_envelope(), headers=auth_headers()))
    assert [e["type"] for e in events] == ["text_delta", "usage", "done"]
    assert events[0]["text"] == "stub completion"
    usage = events[1]
    assert usage["trace_id"] == TRACE_ID
    assert usage["capability"] == "chat"
    assert (usage["configured_provider"], usage["configured_model"]) == ("anthropic", "test-model")
    assert usage["gateway"] == "openrouter"
    assert usage["actual_model"] == "anthropic/test-model-served"
    assert usage["actual_provider"] == "unknown"
    assert len(transport.calls) == 1
    assert transport.calls[0]["payload"]["models"] == ["anthropic/test-model", "openai/test-b"]


def _without(field: str) -> dict[str, Any]:
    envelope = make_envelope()
    del envelope[field]
    return envelope


@pytest.mark.parametrize(
    "envelope",
    [
        _without("authorization_ref"),
        make_envelope(api_key="sk-synthetic-credential"),
        make_envelope(password="synthetic"),
        make_envelope(unexpected_field="x"),
        make_envelope(capability="vision"),
        make_envelope(allowed_tools=[{"name": "records.search", "level": "admin"}]),
        make_envelope(input={"messages": []}),
        make_envelope(organization_id="not-a-uuid"),
    ],
    ids=[
        "missing-authorization-ref",
        "credential-field",
        "password-field",
        "unknown-field",
        "bad-capability",
        "bad-tool-level",
        "empty-messages",
        "bad-organization-id",
    ],
)
def test_i_execute_rejects_invalid_envelope_with_422(make_client, envelope) -> None:
    transport = StubTransport()
    client = make_client(env=CHAT_ENV, transport=transport)
    response = client.post(EXECUTE, json=envelope, headers=auth_headers())
    assert response.status_code == 422
    assert transport.calls == []
    # Rejections name locations and keywords, never submitted values.
    assert "sk-synthetic-credential" not in response.text


def test_i_execute_rejects_non_json_with_422(make_client) -> None:
    client = make_client(env=CHAT_ENV, transport=StubTransport())
    response = client.post(
        EXECUTE,
        content=b"{not json",
        headers={**auth_headers(), "Content-Type": "application/json"},
    )
    assert response.status_code == 422


def test_i_execute_unconfigured_capability_streams_error_then_done(make_client) -> None:
    transport = StubTransport()
    client = make_client(env=CHAT_ENV, transport=transport)
    envelope = make_envelope(capability="reasoning")
    events = _events(client.post(EXECUTE, json=envelope, headers=auth_headers()))
    assert [e["type"] for e in events] == ["error", "done"]
    assert events[0]["code"] == "capability_not_configured"
    assert transport.calls == []


def test_i_execute_zero_capabilities_streams_error(make_client) -> None:
    transport = StubTransport()
    client = make_client(env=base_env(), transport=transport)
    events = _events(client.post(EXECUTE, json=make_envelope(), headers=auth_headers()))
    assert [(e["type"], e.get("code")) for e in events] == [
        ("error", "capability_not_configured"),
        ("done", None),
    ]
    assert transport.calls == []


def test_i_execute_gateway_failure_streams_error_then_done(make_client) -> None:
    transport = StubTransport(error=GatewayError("gateway_error", "synthetic", retryable=True))
    client = make_client(env=CHAT_ENV, transport=transport)
    events = _events(client.post(EXECUTE, json=make_envelope(), headers=auth_headers()))
    assert [e["type"] for e in events] == ["error", "done"]
    assert events[0]["code"] == "gateway_error"
    assert events[0]["retryable"] is True


def test_i_execute_impossible_deadline_is_rejected_with_422(make_client) -> None:
    transport = StubTransport()
    client = make_client(env=CHAT_ENV, transport=transport)
    response = client.post(
        EXECUTE, json=make_envelope(deadline="2026-02-30T25:61:61Z"), headers=auth_headers()
    )
    assert response.status_code == 422
    assert transport.calls == []


def test_i_execute_oversized_body_is_rejected_with_413(make_client) -> None:
    client = make_client(env=CHAT_ENV, transport=StubTransport())
    response = client.post(
        EXECUTE,
        content=b"{" + b" " * (1024 * 1024 + 1) + b"}",
        headers={**auth_headers(), "Content-Type": "application/json"},
    )
    assert response.status_code == 413


def test_i_execute_expired_deadline_makes_no_model_call(make_client) -> None:
    transport = StubTransport()
    client = make_client(env=CHAT_ENV, transport=transport)
    past = (datetime.now(UTC) - timedelta(seconds=1)).isoformat()
    events = _events(
        client.post(EXECUTE, json=make_envelope(deadline=past), headers=auth_headers())
    )
    assert events[0]["code"] == "deadline_exceeded"
    assert transport.calls == []


def test_i_execute_embedding_is_not_served_by_text_path(make_client) -> None:
    transport = StubTransport()
    env = base_env(AI_MODEL_EMBEDDING="openai:test-embedding")
    client = make_client(env=env, transport=transport)
    envelope = make_envelope(capability="embedding")
    events = _events(client.post(EXECUTE, json=envelope, headers=auth_headers()))
    assert [(e["type"], e.get("code")) for e in events] == [
        ("error", "capability_not_supported"),
        ("done", None),
    ]
    assert transport.calls == []
