"""Loads the cross-service JSON Schemas, the single source of the wire contract.

In the image the schemas live under ``/srv/contracts/intelligence`` (copied
from the project's ``contracts/`` directory through a named build context).
Outside the image the project-relative ``contracts/intelligence`` is used.
"""

from __future__ import annotations

import json
from functools import cache
from pathlib import Path
from typing import Any

from jsonschema import Draft202012Validator, FormatChecker

IMAGE_CONTRACTS_DIR = Path("/srv/contracts/intelligence")
# src/streaming/schemas.py -> parents[3] is the project root.
PROJECT_CONTRACTS_DIR = Path(__file__).resolve().parents[3] / "contracts" / "intelligence"

ENVELOPE_SCHEMA = "execution-envelope.schema.json"
STREAM_EVENT_SCHEMA = "stream-event.schema.json"


def contracts_dir() -> Path:
    for candidate in (IMAGE_CONTRACTS_DIR, PROJECT_CONTRACTS_DIR):
        if (candidate / ENVELOPE_SCHEMA).is_file():
            return candidate
    raise FileNotFoundError("intelligence contract schemas not found")


def load_schema(name: str) -> dict[str, Any]:
    with (contracts_dir() / name).open(encoding="utf-8") as fh:
        return json.load(fh)


@cache
def validator(name: str) -> Draft202012Validator:
    """A meta-schema-checked validator with format assertions enabled."""
    schema = load_schema(name)
    Draft202012Validator.check_schema(schema)
    return Draft202012Validator(schema, format_checker=FormatChecker())


def envelope_validator() -> Draft202012Validator:
    return validator(ENVELOPE_SCHEMA)


def event_validator() -> Draft202012Validator:
    return validator(STREAM_EVENT_SCHEMA)
