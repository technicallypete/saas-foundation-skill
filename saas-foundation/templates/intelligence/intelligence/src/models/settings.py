"""Service settings parsed once at startup; invalid configuration fails fast.

Empty values mean "unset". Secrets are held with ``repr=False`` so they never
appear in logs, tracebacks or error messages.
"""

from __future__ import annotations

from collections.abc import Mapping
from dataclasses import dataclass, field

from models.resolver import ConfigError, ModelResolver

APP_ENVS = ("development", "test", "production")


@dataclass(frozen=True, slots=True)
class Settings:
    app_env: str
    resolver: ModelResolver
    service_token: str = field(default="", repr=False)
    openrouter_api_key: str = field(default="", repr=False)

    @classmethod
    def from_env(cls, env: Mapping[str, str]) -> Settings:
        problems: list[str] = []
        app_env = env.get("APP_ENV", "")
        if not app_env:
            problems.append("APP_ENV is required")
        elif app_env not in APP_ENVS:
            problems.append("APP_ENV must be development, test or production")

        resolver: ModelResolver | None = None
        try:
            resolver = ModelResolver.from_env(env)
        except ConfigError as exc:
            problems.extend(exc.problems)

        if problems or resolver is None:
            raise ConfigError(problems)
        return cls(
            app_env=app_env,
            resolver=resolver,
            service_token=env.get("INTELLIGENCE_SERVICE_TOKEN", ""),
            openrouter_api_key=env.get("OPENROUTER_API_KEY", ""),
        )
