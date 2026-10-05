# Invocation checklist

Use this when running the Skill for a user. Paths are relative to the Skill directory.

- [ ] **Preflight:** `docker compose version` reports 2.24 or later; the destination directory exists, is not a symlink and is not this Skill; the host ports to be used are free (otherwise collect `ports` overrides); the user knows a clean git working tree or new branch is recommended, and decides whether to `git init` a destination that is not a repository.
- [ ] **Inputs collected from the user** (never invented): `product_name`, `platform_base_domain`, `database_name`, `intelligence_enabled`; optional values confirmed or defaulted (see [../references/configuration.md](../references/configuration.md)).
- [ ] **Input file written** outside the destination (for example a scratch directory). It contains no secrets.
- [ ] **Validate:** `generator/render.sh validate --input <file>` exits 0 and prints the resolved configuration; show it to the user. On exit 2, fix every reported problem and repeat.
- [ ] **Plan:** `generator/render.sh plan --input <file> --dest <dir>` shows the `create` and `conflict` lines; nothing was written.
- [ ] **User confirmation** obtained for the plan.
- [ ] **Render:** `generator/render.sh render --input <file> --dest <dir> [--report <file outside dest>]`; note the `output_id`. Exit 3 means conflicts to reconcile.
- [ ] **Reconcile** each `conflict`: show `diff <path> .archetype/outputs/<output_id>/proposed/<path>`; apply only what the user approves. Never overwrite blindly; never touch `AGENTS.md` or `CLAUDE.md`.
- [ ] **`.env` created** from `.env.example` with locally generated **hex** secrets (at least 16 characters); not printed, not committed (`.gitignore` contains `.env`).
- [ ] **Start and check** with the executable blocks in `docs/foundation/GETTING-STARTED.md`; all services healthy; checks pass.
- [ ] **Hand-off:** point the user to `docs/foundation/FOLLOW-UP.md` (foundation stages still to implement) and `docs/foundation/VENDORS.md`.
- [ ] **Commit guidance:** commit the rendered files and `.archetype/outputs/<output_id>/manifest.json`; do not commit `.env` or `proposed/` files.
