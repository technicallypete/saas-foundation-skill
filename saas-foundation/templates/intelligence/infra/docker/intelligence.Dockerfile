# syntax=docker/dockerfile:1
# Intelligence (Python/FastAPI) image. Build context: intelligence/.
# The contract schemas come from the named build context "contracts"
# (Compose: additional_contexts; plain build: --build-context contracts=<project>/contracts).
# Targets: runtime, check.
FROM ghcr.io/astral-sh/uv:0.12.23 AS uv

FROM python:3.13.16-slim AS base
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PYTHONPATH=/srv/app/src \
    UV_PYTHON_DOWNLOADS=never \
    UV_PYTHON=/usr/local/bin/python3 \
    UV_PROJECT_ENVIRONMENT=/opt/venv \
    UV_LINK_MODE=copy \
    UV_NO_CACHE=1 \
    PATH=/opt/venv/bin:$PATH
COPY --from=uv /uv /usr/local/bin/uv
RUN groupadd --system --gid 10001 app \
 && useradd --system --uid 10001 --gid app --no-create-home --shell /usr/sbin/nologin app
WORKDIR /srv/app
COPY pyproject.toml uv.lock ./
# Runtime dependencies only, exactly as locked.
RUN uv sync --locked --no-dev
# "contracts" is a named build context, not a stage.
# hadolint ignore=DL3022
COPY --from=contracts intelligence/ /srv/contracts/intelligence/

FROM base AS check
# Dev/test dependencies are installed at build time: the check container runs
# with networking disabled and must not download anything.
RUN uv sync --locked
COPY . .
USER 10001:10001
CMD ["sh", "-c", "ruff check --no-cache . && ruff format --check --no-cache . && pytest -q"]

FROM base AS runtime
COPY src ./src
USER 10001:10001
EXPOSE 8000
CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8000"]
