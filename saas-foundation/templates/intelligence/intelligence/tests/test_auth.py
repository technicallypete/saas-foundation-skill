"""I-AUTH: fail-closed internal authentication and content-free health."""

from __future__ import annotations

import pytest

from conftest import SERVICE_TOKEN, StubTransport, auth_headers, base_env, make_envelope

EXECUTE = "/internal/v1/execute"


@pytest.mark.parametrize(
    "headers",
    [
        {},
        {"Authorization": "Bearer wrong-token"},
        {"Authorization": f"Bearer {SERVICE_TOKEN}x"},
        {"Authorization": f"Basic {SERVICE_TOKEN}"},
        {"Authorization": SERVICE_TOKEN},
        {"Authorization": "Bearer "},
    ],
    ids=["missing", "wrong", "suffixed", "wrong-scheme", "no-scheme", "empty"],
)
def test_i_auth_rejects_missing_or_wrong_token(make_client, headers) -> None:
    transport = StubTransport()
    client = make_client(env=base_env(AI_MODEL_CHAT="openai:test-model"), transport=transport)
    response = client.post(EXECUTE, json=make_envelope(), headers=headers)
    assert response.status_code == 401
    assert response.headers["www-authenticate"] == "Bearer"
    assert transport.calls == []


def test_i_auth_accepts_valid_token(make_client) -> None:
    client = make_client(env=base_env(AI_MODEL_CHAT="openai:test-model"), transport=StubTransport())
    assert client.post(EXECUTE, json=make_envelope(), headers=auth_headers()).status_code == 200


@pytest.mark.parametrize("configured", [None, ""], ids=["unset", "empty"])
@pytest.mark.parametrize("presented", ["", "anything", SERVICE_TOKEN])
def test_i_auth_fails_closed_when_token_unset_or_empty(make_client, configured, presented) -> None:
    env = {"APP_ENV": "test"}
    if configured is not None:
        env["INTELLIGENCE_SERVICE_TOKEN"] = configured
    client = make_client(env=env, transport=StubTransport())
    headers = {"Authorization": f"Bearer {presented}"}
    assert client.post(EXECUTE, json=make_envelope(), headers=headers).status_code == 401
    assert client.get("/internal/anything", headers=headers).status_code == 401


def test_i_auth_runs_before_body_parsing(make_client) -> None:
    client = make_client()
    response = client.post(
        EXECUTE, content=b"{not json", headers={"Content-Type": "application/json"}
    )
    assert response.status_code == 401


def test_i_auth_guards_unknown_internal_paths(make_client) -> None:
    client = make_client()
    assert client.get("/internal/v1/other").status_code == 401
    assert client.get("/internal/v1/other", headers=auth_headers()).status_code == 404


def test_i_auth_health_endpoints_are_unauthenticated_and_content_free(make_client) -> None:
    env = base_env(AI_MODEL_CHAT="anthropic:distinctive-model-name", OPENROUTER_API_KEY="sk-or-x")
    client = make_client(env=env)
    health = client.get("/healthz")
    ready = client.get("/readyz")
    assert (health.status_code, health.json()) == (200, {"status": "ok"})
    assert (ready.status_code, ready.json()) == (200, {"status": "ready"})
    for body in (health.text, ready.text):
        for leaked in ("distinctive-model-name", "anthropic", "sk-or-x", SERVICE_TOKEN, "test"):
            assert leaked not in body


def test_i_auth_no_public_api_docs(make_client) -> None:
    client = make_client()
    for path in ("/docs", "/redoc", "/openapi.json"):
        assert client.get(path).status_code == 404
