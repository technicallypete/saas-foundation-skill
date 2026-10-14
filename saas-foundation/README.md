# SaaS Foundation: usage and validation

This directory is the complete **saas-foundation** skill package. Keep its contents together when copying or installing it. [SKILL.md](SKILL.md) contains the agent workflow, architectural invariants, and input contract.

## Generate a project

Run the following from this package directory. Git, Docker with Compose 2.24 or later, bash, and GNU shell utilities are required. The generator runs in Docker; initial builds download dependencies and can take several minutes.

Use a copy of [examples/neutral.input.json](examples/neutral.input.json) with your own product identity, domain, database name, and explicit intelligence flag. The bundled values below are only a disposable demonstration.

```sh
generator/render.sh validate --input examples/neutral.input.json

# Choose an existing destination outside the skill and distribution repository.
# This example creates a sibling of the distribution repository.
mkdir -p ../../example-project
generator/render.sh plan --input examples/neutral.input.json --dest ../../example-project

# Review the resolved inputs and planned actions before rendering.
generator/render.sh render --input examples/neutral.input.json --dest ../../example-project
```

The renderer preserves existing files. Conflicts produce proposals under `.archetype/outputs/<output_id>/proposed/` for review. It never creates local secrets or overwrites repository instruction files. Exit codes are `0` for success, `1` for an internal error, `2` for invalid input or an unsafe destination, and `3` for conflicts requiring reconciliation.

After rendering, follow the destination's `docs/foundation/GETTING-STARTED.md` to create a local `.env` with disposable credentials, start services, and run checks. Keep credentials out of Git. See the [configuration contract](references/configuration.md) and [Docker runtime guide](references/docker-runtime.md).

## Delivery workflow

Use `sdlc-management` or Restruct SDLC when a supervisor/coordinator delivery workflow
is wanted. It owns lifecycle policy; this package owns foundation architecture and
renderer safety. Ordinary application features use checks for their changed behavior,
not an automatic rerun of the entire generator qualification suite. Keep compact
Git-linked outcomes and purge disposable task-owned evidence after verified closeout.
See [validation and closeout](checklists/verification.md#proportionate-validation-and-closeout);
existing approved security checks and provenance bindings remain required.

## Validate the skill

The harness supports `static`, `generator`, `intelligence`, `isolation`, `trials`, and `all`. Supply a nonempty denylist outside the package containing private project brand/domain terms that must not appear in this generic skill or its output.

For this distribution layout, run from the package directory:

```sh
validation/run.sh all \
  --denylist /path/to/private-terms.txt \
  --repo-allow '^(saas-foundation/|README\.md$|LICENSE$)' \
  --report-dir /path/to/reports
```

Replace the placeholder paths with real locations. Keep reports and generated trial projects outside the repository. The allow-list permits the portable package and the two root distribution documents; tighten it for a narrower change or adjust it explicitly if installing into a different repository layout.

Repository-scope assertions require a Git repository. Without one, the harness can exercise package behavior but cannot establish repository cleanliness. The repository-clean check also rejects generated `web`, `core`, `intelligence`, `contracts`, or `infra` directories at the distribution root.

See the [verification checklist](checklists/verification.md) for suite coverage and evidence limits.

## Foundation limits

A passing run proves the skeleton, renderer safety, and minimal isolation fixtures. Production identity integration, tenant schema/policies, audit wiring, authenticated MCP mounting, and other application foundations remain consumer work. See [foundation follow-up](checklists/foundation-follow-up.md). It does not establish production security or SOC 2 compliance.

Optional local skills described in [commodity composition](references/commodity-composition.md) are not dependencies.

## License

Copyright (c) 2026 Vitruvian Technology, Corp. Licensed under the [MIT License](LICENSE). Keep this file with copied installations. Third-party dependencies and tools retain their own licenses.
