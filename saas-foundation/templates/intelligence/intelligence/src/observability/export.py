"""Privacy-safe usage/trace export hook and service logging.

The default export carries identifiers, configured/actual model identity,
token counts, latency and outcome only. It never carries prompts,
completions, retrieved content, envelope input or secrets. Content capture
requires constructing the hook with ``capture_content=True`` explicitly; there
is no implicit or environment-driven way to turn it on.

Vendor observability is never the usage ledger: core records durable usage by
joining the ``usage`` stream event to the envelope it issued.
"""

from __future__ import annotations

import json
import logging
import sys
from collections.abc import Callable, Mapping, Sequence
from dataclasses import asdict
from typing import Any

from providers import Message, UsageRecord

LOGGER_NAME = "intelligence"
EXPORT_LOGGER_NAME = "intelligence.observability"

Sink = Callable[[Mapping[str, Any]], None]


def log_sink(record: Mapping[str, Any]) -> None:
    """Default sink: one structured log line per execution."""
    logging.getLogger(EXPORT_LOGGER_NAME).info("execution", extra={"fields": dict(record)})


class ObservabilityHook:
    """Builds and exports one trace record per execution."""

    def __init__(self, sink: Sink = log_sink, *, capture_content: bool = False) -> None:
        self._sink = sink
        self.capture_content = capture_content

    def record(
        self,
        *,
        request_id: str,
        trace_id: str,
        capability: str,
        outcome: str,
        usage: UsageRecord | None = None,
        messages: Sequence[Message] = (),
        completion: str | None = None,
    ) -> dict[str, Any]:
        """Export a record; return it (tests inspect what was exported)."""
        record: dict[str, Any] = {
            "request_id": request_id,
            "trace_id": trace_id,
            "capability": capability,
            "outcome": outcome,
        }
        if usage is not None:
            record["usage"] = asdict(usage)
        if self.capture_content:
            record["content"] = {
                "messages": [{"role": m.role, "content": m.content} for m in messages],
                "completion": completion,
            }
        self._sink(record)
        return record


class JsonFormatter(logging.Formatter):
    """JSON log lines: timestamp, level, logger, message and explicit fields only."""

    def format(self, record: logging.LogRecord) -> str:
        payload: dict[str, Any] = {
            "time": self.formatTime(record, "%Y-%m-%dT%H:%M:%S%z"),
            "level": record.levelname.lower(),
            "logger": record.name,
            "msg": record.getMessage(),
        }
        fields = getattr(record, "fields", None)
        if isinstance(fields, Mapping):
            payload.update(fields)
        if record.exc_info and record.exc_info[0] is not None:
            # Exception type only: messages and tracebacks may carry content.
            payload["exception"] = record.exc_info[0].__name__
        return json.dumps(payload, default=str)


def configure_logging(level: int = logging.INFO) -> None:
    """Route service logs to stdout as JSON. Idempotent."""
    logger = logging.getLogger(LOGGER_NAME)
    if any(getattr(h, "_intelligence", False) for h in logger.handlers):
        return
    handler = logging.StreamHandler(sys.stdout)
    handler.setFormatter(JsonFormatter())
    handler._intelligence = True  # type: ignore[attr-defined]
    logger.addHandler(handler)
    logger.setLevel(level)
