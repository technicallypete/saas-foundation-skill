"""Typed stream events and NDJSON serialization.

Each constructor returns a plain dict shaped exactly as
``stream-event.schema.json`` requires; the schema is the contract and the
test suite validates every emitted event against it.
"""

from __future__ import annotations

import json
from collections.abc import Mapping
from typing import Any

from providers import UsageRecord

MEDIA_TYPE = "application/x-ndjson"

Event = dict[str, Any]


def text_delta(request_id: str, text: str) -> Event:
    return {"type": "text_delta", "request_id": request_id, "text": text}


def tool_call(request_id: str, call_id: str, name: str, arguments: Mapping[str, Any]) -> Event:
    return {
        "type": "tool_call",
        "request_id": request_id,
        "call_id": call_id,
        "name": name,
        "arguments": dict(arguments),
    }


def tool_result(request_id: str, call_id: str, content: Any, *, is_error: bool = False) -> Event:
    return {
        "type": "tool_result",
        "request_id": request_id,
        "call_id": call_id,
        "is_error": is_error,
        "content": content,
    }


def citation(request_id: str, kind: str, ref: str, locator: str | None = None) -> Event:
    event: Event = {"type": "citation", "request_id": request_id, "kind": kind, "ref": ref}
    if locator is not None:
        event["locator"] = locator
    return event


def structured_output(
    request_id: str, data: Mapping[str, Any], schema_ref: str | None = None
) -> Event:
    event: Event = {"type": "structured_output", "request_id": request_id, "data": dict(data)}
    if schema_ref is not None:
        event["schema_ref"] = schema_ref
    return event


def usage(request_id: str, trace_id: str, capability: str, record: UsageRecord) -> Event:
    """Usage event. Deliberately has no tenant, user or content fields."""
    return {
        "type": "usage",
        "request_id": request_id,
        "trace_id": trace_id,
        "capability": capability,
        "configured_provider": record.configured_provider,
        "configured_model": record.configured_model,
        "gateway": record.gateway,
        "actual_provider": record.actual_provider,
        "actual_model": record.actual_model,
        "input_tokens": record.input_tokens,
        "output_tokens": record.output_tokens,
        "latency_ms": record.latency_ms,
        "estimated_cost": record.estimated_cost,
    }


def error(request_id: str, code: str, message: str, *, retryable: bool = False) -> Event:
    return {
        "type": "error",
        "request_id": request_id,
        "code": code,
        "message": message,
        "retryable": retryable,
    }


def done(request_id: str) -> Event:
    return {"type": "done", "request_id": request_id}


def to_ndjson(event: Mapping[str, Any]) -> bytes:
    """Serialize one event as a single NDJSON line."""
    return (json.dumps(event, separators=(",", ":"), ensure_ascii=False) + "\n").encode("utf-8")
