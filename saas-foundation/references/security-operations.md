# Security operations and SOC 2 readiness hooks

These are engineering foundations that make a later SOC 2 program feasible. **They are not a compliance claim:** compliance depends on organizational controls, evidence and operating effectiveness.

| Control area | In the skeleton (verified) | Consumer obligation (stage) |
|---|---|---|
| Secrets boundary | Core reads secrets through a `SecretProvider` interface (environment-backed locally); `.env` is never committed; every secret is empty in `.env.example`; per-service secret scoping in Compose | Production secrets manager behind the same interface; rotation (STAGE-OPS) |
| Redacted structured logs | `log/slog` JSON with a redacting handler (sensitive keys and known secret values); no headers, bodies or query strings logged | Central log pipeline; retention (STAGE-OBSERVABILITY) |
| Health and readiness | Core, web and intelligence liveness/readiness endpoints; dependency outage reported by name | Alerting on readiness (STAGE-OBSERVABILITY) |
| Traces and metrics | Request IDs and W3C `traceparent` correlation in core | Metrics export, trace propagation across services (STAGE-OBSERVABILITY) |
| Access provisioning | Separate database roles; scoped object-store credential; RoleBinding single authority store | Provisioning/deprovisioning workflows and evidence (STAGE-OPS) |
| Scanning | gitleaks in the `checks` profile (no network) | Dependency and vulnerability scanning (govulncheck, npm audit, pip-audit, image scanning) with pinned or cached databases; failing scans block CI (STAGE-OPS) |
| CI and change evidence | Containerized checks that CI can run unchanged | CI pipeline, reviews, change records (STAGE-OBSERVABILITY, STAGE-OPS) |
| Encryption | Loopback-only local ports; secrets never in images | TLS in transit, encryption at rest for databases, object storage and backups (STAGE-OPS) |
| Backup and restore | Fixture evidence: logical dump restored into a fresh instance from the same role script keeps RLS, grants and data | Scheduled backups for PostgreSQL and object storage; periodic restore tests into isolated environments (STAGE-OPS) |
| Retention and deletion | Audit retention role and policy (expired rows only) | Data retention schedules; authorization-safe deletion (STAGE-OPS) |
| Audit | Controlled AuditEvent model ([storage-audit.md](storage-audit.md)) | Completeness per event category (STAGE-AUDIT) |
| Incident evidence | Correlated logs and audit events | Incident runbooks, monitoring and alert evidence (STAGE-OPS) |
| Vendor inventory | Rendered `docs/foundation/VENDORS.md` (including the local MinIO status and Redis licence notes) | Keep current; record data handling and sub-processors (STAGE-OPS) |
| Cross-tenant tests | Isolation fixtures (runtime-role RLS, policy, MCP, storage) | Full attack suite over real services (STAGE-RLS, STAGE-POLICY, STAGE-MCP) |
| AI data isolation and trace privacy | Envelope contract, no tenant dimensions in usage events, content-free observability by default | Retrieval filtering, durable usage ledger, trace privacy tests (STAGE-AI) |

## Development-only adapters

Any development-only identity or authentication adapter must live behind a build tag or explicit flag, be excluded from production builds, and refuse to start when `APP_ENV=production` (STAGE-IDENTITY). The fixture authenticator in this Skill exists only in test files and is never rendered.

## Supply chain

Pin images and dependencies; commit lockfiles (`go.sum`, `package-lock.json`, `uv.lock`); record image digests in change evidence. The local MinIO server is unmaintained upstream; re-evaluate before any non-local use and prefer a maintained S3-compatible service in shared environments, subject to an architecture decision.
