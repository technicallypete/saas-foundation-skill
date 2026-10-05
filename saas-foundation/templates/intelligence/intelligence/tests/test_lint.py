"""I-LINT: ruff lint and format checks over the service and its tests."""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path

import pytest

PROJECT = Path(__file__).resolve().parents[1]


@pytest.mark.parametrize(
    "args",
    [["check", "--no-cache", "."], ["format", "--check", "--no-cache", "."]],
    ids=["ruff-check", "ruff-format-check"],
)
def test_i_lint_ruff(args: list[str]) -> None:
    result = subprocess.run(
        [sys.executable, "-m", "ruff", *args],
        cwd=PROJECT,
        capture_output=True,
        text=True,
        check=False,
    )
    assert result.returncode == 0, result.stdout + result.stderr
