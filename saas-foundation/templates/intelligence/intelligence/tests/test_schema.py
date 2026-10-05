"""I-SCHEMA: contract schemas, good samples and bad samples."""

from __future__ import annotations

import copy
from typing import Any

import pytest
from jsonschema import Draft202012Validator

from conftest import REQUEST_ID, TRACE_ID, make_envelope
from providers import UsageRecord
from streaming import envelope_validator, event_validator, events
from streaming.schemas import (
    ENVELOPE_SCHEMA,
    IMAGE_CONTRACTS_DIR,
    STREAM_EVENT_SCHEMA,
    contracts_dir,
    load_schema,
)

USAGE = UsageRecord(
    configured_provider="anthropic",
    configured_model="test-model",
    gateway="openrouter",
    actual_provider="unknown",
    actual_model="anthropic/test-model",
    input_tokens=10,
    output_tokens=4,
    latency_ms=12,
    estimated_cost=None,
)


def _valid(validator: Draft202012Validator, doc: Any) -> bool:
    return not list(validator.iter_errors(doc))


def test_i_schema_loaded_from_image_contracts_location() -> None:
    assert contracts_dir() == IMAGE_CONTRACTS_DIR


@pytest.mark.parametrize("name", [ENVELOPE_SCHEMA, STREAM_EVENT_SCHEMA])
def test_i_schema_valid_against_meta_schema(name: str) -> None:
    schema = load_schema(name)
    assert schema["$schema"] == "https://json-schema.org/draft/2020-12/schema"
    Draft202012Validator.check_schema(schema)


def test_i_schema_good_envelopes_pass() -> None:
    validator = envelope_validator()
    assert _valid(validator, make_envelope())
    full = make_envelope(
        group_id="9a8b7c6d-5e4f-4a3b-9c2d-1e0f9a8b7c6d",
        business_entity_id="1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d",
        callback_grant_ref="grant-ref-synthetic",
        allowed_tools=[
            {"name": "records.search", "level": "read"},
            {"name": "records.draft", "level": "propose"},
            {"name": "records.apply", "level": "commit"},
        ],
        context_refs=[],
        deadline="2030-01-01T00:00:00.123+02:00",
    )
    assert _valid(validator, full)


ENVELOPE_REQUIRED = [
    "request_id",
    "trace_id",
    "capability",
    "authorization_ref",
    "organization_id",
    "user_id",
    "allowed_tools",
    "deadline",
    "context_refs",
    "input",
]


@pytest.mark.parametrize("field", ENVELOPE_REQUIRED)
def test_i_schema_envelope_missing_required_field_fails(field: str) -> None:
    envelope = make_envelope()
    del envelope[field]
    assert not _valid(envelope_validator(), envelope)


@pytest.mark.parametrize(
    "overrides",
    [
        {"api_key": "sk-synthetic"},
        {"access_token": "synthetic"},
        {"capability": "vision"},
        {"trace_id": "XYZ"},
        {"request_id": "short"},
        {"deadline": "tomorrow"},
        {"deadline": "2030-01-01 00:00:00"},
        {"user_id": "user-1"},
        {"allowed_tools": [{"name": "records.search", "level": "owner"}]},
        {"allowed_tools": [{"name": "bad name", "level": "read"}]},
        {"allowed_tools": [{"name": "x", "level": "read", "token": "t"}]},
        {"context_refs": [{"kind": "document"}]},
        {"input": {"messages": [{"role": "tool", "content": "x"}]}},
        {"input": {"messages": [{"role": "user", "content": "x"}], "api_key": "k"}},
        {"authorization_ref": ""},
    ],
)
def test_i_schema_bad_envelopes_fail(overrides: dict[str, Any]) -> None:
    assert not _valid(envelope_validator(), make_envelope(**overrides))


GOOD_EVENTS = [
    events.text_delta(REQUEST_ID, "partial"),
    events.tool_call(REQUEST_ID, "call-1", "records.search", {"query": "x"}),
    events.tool_result(REQUEST_ID, "call-1", {"items": []}),
    events.citation(REQUEST_ID, "document", "doc-1", locator="p.2"),
    events.structured_output(REQUEST_ID, {"field": 1}, schema_ref="urn:example:schema"),
    events.usage(REQUEST_ID, TRACE_ID, "chat", USAGE),
    events.error(REQUEST_ID, "capability_not_configured", "not configured"),
    events.done(REQUEST_ID),
]


@pytest.mark.parametrize("event", GOOD_EVENTS, ids=[e["type"] for e in GOOD_EVENTS])
def test_i_schema_good_events_pass(event: dict[str, Any]) -> None:
    assert _valid(event_validator(), event)


def test_i_schema_covers_every_event_type() -> None:
    assert {e["type"] for e in GOOD_EVENTS} == {
        "text_delta",
        "tool_call",
        "tool_result",
        "citation",
        "structured_output",
        "usage",
        "error",
        "done",
    }


def _usage_with(**extra: Any) -> dict[str, Any]:
    event = copy.deepcopy(events.usage(REQUEST_ID, TRACE_ID, "chat", USAGE))
    event.update(extra)
    return event


@pytest.mark.parametrize(
    "dimension",
    ["organization_id", "user_id", "group_id", "business_entity_id"],
)
def test_i_schema_usage_with_tenant_or_user_dimension_fails(dimension: str) -> None:
    event = _usage_with(**{dimension: "0b7c1a52-9a43-4c6e-9f0e-6f1f2d3c4b5a"})
    assert not _valid(event_validator(), event)


def _without(event: dict[str, Any], field: str) -> dict[str, Any]:
    event = copy.deepcopy(event)
    del event[field]
    return event


@pytest.mark.parametrize(
    "event",
    [
        _without(events.done(REQUEST_ID), "request_id"),
        _without(events.text_delta(REQUEST_ID, "x"), "type"),
        {"type": "unknown", "request_id": REQUEST_ID},
        {**events.text_delta(REQUEST_ID, "x"), "extra": 1},
        _without(events.error(REQUEST_ID, "c", "m"), "code"),
        _without(_usage_with(), "trace_id"),
        _without(_usage_with(), "actual_provider"),
        _usage_with(prompt="leaked content"),
        _usage_with(input_tokens=-1),
        {**events.done(REQUEST_ID), "request_id": "bad id with spaces"},
    ],
    ids=[
        "done-without-request-id",
        "without-type",
        "unknown-type",
        "extra-field",
        "error-without-code",
        "usage-without-trace-id",
        "usage-without-actual-provider",
        "usage-with-content",
        "usage-negative-tokens",
        "bad-request-id",
    ],
)
def test_i_schema_bad_events_fail(event: dict[str, Any]) -> None:
    assert not _valid(event_validator(), event)
