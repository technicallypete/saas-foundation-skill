"""Intelligence service entry point (``uvicorn main:app`` with ``PYTHONPATH=src``).

Internal inference only: core authorizes every execution and sends an
execution envelope; this service resolves the capability to a configured
model, calls it through the ModelGateway boundary and streams typed NDJSON
events back. It holds no database, cache or object-store credentials.
"""

from __future__ import annotations

import json
import logging
import os
from collections.abc import AsyncIterator, Mapping
from datetime import UTC, datetime
from typing import Annotated, Any

from fastapi import APIRouter, Depends, FastAPI, HTTPException, Request, status
from fastapi.responses import StreamingResponse

from models import Capability, Settings
from observability import LOGGER_NAME, ObservabilityHook, configure_logging
from providers import (
    CompletionRequest,
    GatewayError,
    HttpxTransport,
    Message,
    ModelGateway,
    OpenRouterGateway,
)
from security import require_service_token
from streaming import envelope_validator, event_validator, events

MAX_BODY_BYTES = 1024 * 1024
MAX_GATEWAY_TIMEOUT_S = 120.0

# Capabilities the text completion path serves. Embeddings need a dedicated
# adapter method, which is an extension point for a later stage.
TEXT_CAPABILITIES = frozenset(
    {Capability.CHAT, Capability.FAST, Capability.REASONING, Capability.EXTRACTION}
)

logger = logging.getLogger(LOGGER_NAME)


def get_settings(request: Request) -> Settings:
    return request.app.state.settings


def get_gateway(request: Request) -> ModelGateway:
    return request.app.state.gateway


def get_observability(request: Request) -> ObservabilityHook:
    return request.app.state.observability


def _validation_problems(document: Any) -> list[dict[str, str]]:
    """Schema errors as JSON pointers and keywords only; values are never echoed."""
    problems = []
    for err in sorted(envelope_validator().iter_errors(document), key=lambda e: list(e.path)):
        pointer = "/" + "/".join(str(p) for p in err.absolute_path)
        problems.append({"path": pointer, "keyword": str(err.validator)})
    return problems


def _parse_deadline(value: str) -> datetime:
    deadline = datetime.fromisoformat(value.replace("Z", "+00:00"))
    return deadline if deadline.tzinfo else deadline.replace(tzinfo=UTC)


internal = APIRouter(prefix="/internal", dependencies=[Depends(require_service_token)])


@internal.post("/v1/execute")
async def execute(
    request: Request,
    settings: Annotated[Settings, Depends(get_settings)],
    gateway: Annotated[ModelGateway, Depends(get_gateway)],
    hook: Annotated[ObservabilityHook, Depends(get_observability)],
) -> StreamingResponse:
    # Enforce the size limit while reading, never after buffering everything.
    declared = request.headers.get("content-length", "")
    if declared.isdigit() and int(declared) > MAX_BODY_BYTES:
        raise HTTPException(status.HTTP_413_REQUEST_ENTITY_TOO_LARGE, "envelope too large")
    body = bytearray()
    async for chunk in request.stream():
        body.extend(chunk)
        if len(body) > MAX_BODY_BYTES:
            raise HTTPException(status.HTTP_413_REQUEST_ENTITY_TOO_LARGE, "envelope too large")
    try:
        envelope = json.loads(body)
    except ValueError:
        raise HTTPException(status.HTTP_422_UNPROCESSABLE_CONTENT, "invalid JSON") from None
    problems = _validation_problems(envelope)
    if problems:
        raise HTTPException(
            status.HTTP_422_UNPROCESSABLE_CONTENT,
            {
                "message": "envelope does not match the execution envelope schema",
                "errors": problems,
            },
        )

    request_id: str = envelope["request_id"]
    trace_id: str = envelope["trace_id"]
    capability = Capability(envelope["capability"])
    messages = tuple(Message(m["role"], m["content"]) for m in envelope["input"]["messages"])
    try:
        deadline = _parse_deadline(envelope["deadline"])
    except ValueError:
        # The schema pattern admits impossible calendar values; reject them as
        # an invalid envelope rather than failing the request.
        raise HTTPException(
            status.HTTP_422_UNPROCESSABLE_CONTENT,
            {
                "message": "envelope does not match the execution envelope schema",
                "errors": [{"path": "/deadline", "keyword": "format"}],
            },
        ) from None
    remaining_s = (deadline - datetime.now(UTC)).total_seconds()

    def finish(outcome: str, **extra: Any) -> None:
        hook.record(
            request_id=request_id,
            trace_id=trace_id,
            capability=capability.value,
            outcome=outcome,
            messages=messages,
            **extra,
        )

    async def stream() -> AsyncIterator[bytes]:
        if remaining_s <= 0:
            finish("deadline_exceeded")
            yield events.to_ndjson(
                events.error(request_id, "deadline_exceeded", "the envelope deadline has passed")
            )
        elif not settings.resolver.is_configured(capability):
            finish("capability_not_configured")
            yield events.to_ndjson(
                events.error(
                    request_id,
                    "capability_not_configured",
                    f"capability {capability.value!r} is not configured",
                )
            )
        elif capability not in TEXT_CAPABILITIES:
            finish("capability_not_supported")
            yield events.to_ndjson(
                events.error(
                    request_id,
                    "capability_not_supported",
                    f"capability {capability.value!r} is not served by this endpoint",
                )
            )
        else:
            route = settings.resolver.resolve(capability)
            completion = CompletionRequest(
                route=route,
                messages=messages,
                timeout_s=min(remaining_s, MAX_GATEWAY_TIMEOUT_S),
            )
            try:
                result = await gateway.complete(completion)
            except GatewayError as exc:
                finish(exc.code)
                yield events.to_ndjson(
                    events.error(request_id, exc.code, str(exc), retryable=exc.retryable)
                )
            else:
                finish("ok", usage=result.usage, completion=result.text)
                yield events.to_ndjson(events.text_delta(request_id, result.text))
                yield events.to_ndjson(
                    events.usage(request_id, trace_id, capability.value, result.usage)
                )
        yield events.to_ndjson(events.done(request_id))

    return StreamingResponse(stream(), media_type=events.MEDIA_TYPE)


@internal.api_route(
    "/{path:path}", methods=["GET", "POST", "PUT", "PATCH", "DELETE"], include_in_schema=False
)
async def internal_not_found(path: str) -> None:
    # Unknown internal paths are still authenticated first (router dependency).
    raise HTTPException(status.HTTP_404_NOT_FOUND, "not found")


def create_app(
    env: Mapping[str, str] | None = None,
    *,
    gateway: ModelGateway | None = None,
    observability: ObservabilityHook | None = None,
) -> FastAPI:
    """Build the application, failing fast on invalid configuration."""
    configure_logging()
    settings = Settings.from_env(os.environ if env is None else env)
    # Load and meta-validate both contract schemas at startup.
    envelope_validator()
    event_validator()

    app = FastAPI(title="intelligence", docs_url=None, redoc_url=None, openapi_url=None)
    app.state.settings = settings
    app.state.gateway = gateway or OpenRouterGateway(HttpxTransport(settings.openrouter_api_key))
    app.state.observability = observability or ObservabilityHook()

    @app.get("/healthz")
    async def healthz() -> dict[str, str]:
        return {"status": "ok"}

    @app.get("/readyz")
    async def readyz() -> dict[str, str]:
        return {"status": "ready"}

    app.include_router(internal)

    if not settings.service_token:
        logger.warning("INTELLIGENCE_SERVICE_TOKEN is not set; /internal/* refuses all requests")
    logger.info(
        "configured",
        extra={"fields": {"capabilities": [c.value for c in settings.resolver.configured]}},
    )
    return app


_app: FastAPI | None = None


def __getattr__(name: str) -> FastAPI:
    # ``main:app`` is built on first access rather than at import, so tests can
    # import this module and build apps from explicit environments.
    global _app
    if name != "app":
        raise AttributeError(name)
    if _app is None:
        _app = create_app()
    return _app
