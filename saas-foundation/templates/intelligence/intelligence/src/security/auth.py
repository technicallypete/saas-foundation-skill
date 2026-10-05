"""Internal service authentication for ``/internal/*`` routes.

Callers present ``Authorization: Bearer <INTELLIGENCE_SERVICE_TOKEN>``. The
comparison is constant-time. When the configured token is unset or empty the
dependency refuses every request (fail closed). The static token is a
fixture-level default; signed, short-lived, audience-bound envelopes are a
later hardening step.
"""

from __future__ import annotations

import hmac

from fastapi import HTTPException, Request, status

_CHALLENGE = {"WWW-Authenticate": "Bearer"}


def _unauthorized() -> HTTPException:
    # Identical response for every failure so callers learn nothing about
    # whether a token is configured.
    return HTTPException(
        status_code=status.HTTP_401_UNAUTHORIZED, detail="unauthorized", headers=_CHALLENGE
    )


def token_matches(presented: str, expected: str) -> bool:
    """Constant-time comparison that never accepts an empty expected token."""
    if not expected:
        return False
    return hmac.compare_digest(presented.encode("utf-8"), expected.encode("utf-8"))


async def require_service_token(request: Request) -> None:
    """FastAPI dependency guarding internal routes."""
    expected: str = request.app.state.settings.service_token
    header = request.headers.get("authorization", "")
    scheme, _, presented = header.partition(" ")
    if scheme.lower() != "bearer" or not presented:
        raise _unauthorized()
    if not token_matches(presented.strip(), expected):
        raise _unauthorized()
