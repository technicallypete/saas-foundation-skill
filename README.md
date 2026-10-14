# SaaS Foundation

A Docker-first, multi-tenant SaaS archetype for Go, Next.js, and optional Python intelligence. By Vitruvian Technology, Corp.

This repository distributes the **saas-foundation** agent skill: a portable package containing instructions, a generator, templates, architecture references, and validation fixtures.

## What it provides

- A Go core, Next.js web surface, and optional internal Python/FastAPI intelligence service.
- PostgreSQL, Redis, and local S3-compatible storage in Docker Compose.
- Configuration-driven product identity, domains, database names, and ports.
- Scoped credentials, service network boundaries, health/readiness, and containerized checks.
- Architectural guidance for tenant isolation, shared authorization, audit, storage, and authenticated MCP foundations.

The output is a runnable skeleton. Isolation fixtures demonstrate contracts; they are not production authentication or authorization. Remaining consumer work is documented in the [foundation follow-up checklist](saas-foundation/checklists/foundation-follow-up.md).

## Repository layout

```text
saas-foundation-skill/
├── README.md
├── LICENSE
└── saas-foundation/
    ├── SKILL.md
    ├── README.md
    ├── LICENSE
    ├── generator/
    ├── templates/
    ├── references/
    ├── checklists/
    ├── examples/
    └── validation/
```

## Installation

Copy the entire `saas-foundation/` directory into your agent's supported skill location. Keep the directory name and all its contents, including LICENSE. Skill discovery locations depend on the agent.

Ask the agent to use **saas-foundation** to bootstrap your project. It follows [SKILL.md](saas-foundation/SKILL.md), collects the required inputs, previews the render plan, and confirms before writing.

The generator can also run directly. See the [package usage guide](saas-foundation/README.md) for commands and validation.

## Delivery policy

Use the optional `sdlc-management` skill or Restruct SDLC for delivery lifecycle,
Git-linked provenance and bounded cleanup. Validation follows changed behavior and
dependencies; full archetype qualification is not the default for application
feature updates. Existing approved security and input bindings remain mandatory.
See [validation and closeout](saas-foundation/checklists/verification.md#proportionate-validation-and-closeout).

## Requirements

Git, Docker with Compose 2.24 or later, bash, and the GNU shell utilities used by the scripts. Initial image and dependency downloads need network access. Project builds and tests run in containers; host Go, Python, Node, PostgreSQL, Redis, and MinIO installations are not required.

## License

Copyright (c) 2026 Vitruvian Technology, Corp. Licensed under the [MIT License](LICENSE). An identical [license copy](saas-foundation/LICENSE) travels with the portable skill package.

The grant covers original package code, documentation, and templates, including that material copied into generated projects. Preserve the copyright and permission notice when redistributing copies or substantial portions. Third-party dependencies and tools retain their own licenses; the package license does not relicense them.
