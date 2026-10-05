"""I-OBSERVABILITY: default export and logs carry no content or secrets."""

from __future__ import annotations

import json
import logging
from typing import Any

from conftest import StubTransport, auth_headers, base_env, make_envelope
from observability import ObservabilityHook
from observability.export import JsonFormatter

PROMPT = "PROMPT-SENTINEL-7f3a"
SYSTEM = "SYSTEM-SENTINEL-1c9e"
COMPLETION = "COMPLETION-SENTINEL-b42d"
CONTEXT = "CONTEXT-SENTINEL-55aa"
# Deliberately key-shaped synthetic sentinel (not a credential): proves redaction.
API_KEY = "sk-or-SENTINEL-KEY-9d0c"  # gitleaks:allow
TOKEN = "TOKEN-SENTINEL-service-0e1f"
SENTINELS = (PROMPT, SYSTEM, COMPLETION, CONTEXT, API_KEY, TOKEN)


def _run(make_client, hook: ObservabilityHook) -> None:
    env = base_env(
        AI_MODEL_CHAT="anthropic:test-model",
        OPENROUTER_API_KEY=API_KEY,
        INTELLIGENCE_SERVICE_TOKEN=TOKEN,
    )
    transport = StubTransport(
        {
            "model": "anthropic/test-model",
            "choices": [{"message": {"content": COMPLETION}}],
            "usage": {"prompt_tokens": 3, "completion_tokens": 2},
        }
    )
    client = make_client(env=env, transport=transport, hook=hook)
    envelope = make_envelope(
        input={
            "messages": [{"role": "system", "content": SYSTEM}, {"role": "user", "content": PROMPT}]
        },
        context_refs=[{"kind": "document", "ref": CONTEXT}],
    )
    response = client.post(EXECUTE, json=envelope, headers=auth_headers(TOKEN))
    assert response.status_code == 200
    assert COMPLETION in response.text  # the caller does receive the completion


EXECUTE = "/internal/v1/execute"


def _captured_logs(caplog) -> str:
    formatter = JsonFormatter()
    return "\n".join(formatter.format(r) for r in caplog.records)


def test_i_observability_default_export_and_logs_have_no_content(
    make_client, caplog, capsys
) -> None:
    caplog.set_level(logging.DEBUG)
    exported: list[dict[str, Any]] = []
    hook = ObservabilityHook()  # default: log sink, capture off

    def spy(record):
        exported.append(dict(record))
        return original(record)

    original = hook._sink
    hook._sink = spy

    _run(make_client, hook)

    assert len(exported) == 1
    record = exported[0]
    assert record["outcome"] == "ok"
    assert record["usage"]["actual_model"] == "anthropic/test-model"
    assert "content" not in record

    surfaces = {
        "export": json.dumps(exported),
        "logs": _captured_logs(caplog),
        "stdout": capsys.readouterr().out,
    }
    assert "execution" in surfaces["logs"]  # the export really went to the logs
    for name, text in surfaces.items():
        for sentinel in SENTINELS:
            assert sentinel not in text, f"{sentinel} leaked into {name}"


def test_i_observability_content_capture_requires_explicit_flag(make_client) -> None:
    exported: list[dict[str, Any]] = []
    _run(make_client, ObservabilityHook(sink=exported.append, capture_content=True))
    text = json.dumps(exported)
    assert PROMPT in text and SYSTEM in text and COMPLETION in text
    for secret in (API_KEY, TOKEN):
        assert secret not in text


def test_i_observability_capture_defaults_off() -> None:
    assert ObservabilityHook().capture_content is False
