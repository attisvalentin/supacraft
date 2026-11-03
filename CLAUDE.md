# CLAUDE.md

SupaCraft: one Go binary that turns `supacraft.json` into a REST backend (SQLite, auth, files, pipeline functions, cron) and exposes it to AI agents over MCP.

## Commands

```bash
go build -o supacraft .
go test ./...
go vet ./...
```

## Layout

- `cmd/` — Cobra commands: `init`, `dev`, `serve`, `mcp`, `validate`, `apply`, `studio`, `service`, `deploy`, `secrets`, `tenants`, `users`, `openapi`, `seed`, `test`, `jobs`, `realtime`.
- Two layouts share the same runtime: a project's own `.supacraft/` (one tenant), or a central data directory (`serve --data-dir`) holding many tenants, picked per request by `X-Tenant-ID`. Never prune or overwrite other tenants in a shared directory (`Project.ApplyTo`).
- `internal/project/` — loads `supacraft.json`, validates it (`validate.go`), compiles it into the tenant config the server reads (`compile.go`: names become ids, `${ENV}` refs resolved from `.env`, auth `users` columns injected) and applies it (`apply.go`: additive SQLite DDL + `.supacraft/tenants/<id>.json`).
- `internal/mcpserver/` — MCP tools. Every write goes through `mutate`: validate → save → apply → reload; invalid changes are not saved. The agent must be able to do whatever the CLI can: a new CLI capability gets an MCP tool too. Central-server changes and destructive actions go through `approved` (an elicitation prompt only the user can answer, bound to the exact action); secret values never pass through MCP.
- `internal/service/` — systemd install/uninstall/status, shared by the CLI and MCP (output goes to a writer: in `supacraft mcp` stdout is the protocol).
- `internal/app/` — starts the API server in-process.
- `internal/docs/reference.md` — the config reference served to agents. Keep it in sync with `internal/tenant` models and `internal/pipeline` step types.
- API runtime: `handler`, `middleware`, `pipeline`, `query`, `schema`, `storage`, `tenant`, `cronjob`, `errorlog`.
- `internal/jobs/` — the background job worker (started with cron) and webhook delivery; the queue itself is `tenant/jobs.go` (`_supacraft_jobs` in each tenant DB), filled by the `job.enqueue` step and by `tenant.EmitWebhooks`. Any new code path that writes table rows must call `tenant.EmitChanges` (webhooks + the realtime change log), with the transaction when there is one.
- `internal/realtime/` — the WebSocket change feed (`GET /realtime`, `handler/realtime.go`): it tails `_supacraft_changes` (`tenant/changes.go`) and sends each subscriber the rows its `query` access rules allow.
- `internal/testrun/` — `seed` and `tests` (never deployed). Tests run each request on a fresh seeded database in a temp dir with a `pipeline.Sandbox` in the request context: email is captured and `http.request` only reaches `http_mocks`. Anything new that talks to the outside world must honour the sandbox.
- `sqlitedb/` and `sqlitedb/ddl` — provisioning and additive migrations. `studio/` — read-only data browser.

## Rules

- Everything is identified by name (tables, columns, functions, cronjobs); compiled UUID fields equal the name.
- Migrations are additive only; never emit DROP/RENAME.
- Quote SQL identifiers with `query.QuoteIdent`, never Go's `%q`. Validate client-supplied column names against the schema before they reach SQL.
- Secret values never go into tenant configs, deploy payloads, API or MCP responses, or logs. A central server keeps `${NAME}` refs in `tenants/<id>.json` and resolves them from `env/<id>.json` on load (`internal/secrets`, `FileStore`). Report secrets by name only.
- Anything reachable over HTTP is untrusted: no internal error details in responses (`writeErrorJSON` trims them), and new routes go under the tenant group so they get tenant validation, rate limits and the body cap.
- In `supacraft mcp`, stdout is the MCP transport — log to stderr only.
- A new pipeline step type needs: execution in `internal/pipeline/engine.go`, validation in `internal/project/validate.go`, docs in `internal/docs/reference.md`.
