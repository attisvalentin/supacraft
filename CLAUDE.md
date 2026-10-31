# CLAUDE.md

SupaCraft: one Go binary that turns `supacraft.json` into a REST backend (SQLite, auth, files, pipeline functions, cron) and exposes it to AI agents over MCP.

## Commands

```bash
go build -o supacraft .
go test ./...
go vet ./...
```

## Layout

- `cmd/` — Cobra commands: `init`, `dev`, `serve`, `mcp`, `validate`, `apply`, `studio`, `service`, `tenants`.
- Two layouts share the same runtime: a project's own `.supacraft/` (one tenant), or a central data directory (`serve --data-dir`) holding many tenants, picked per request by `X-Tenant-ID`. Never prune or overwrite other tenants in a shared directory (`Project.ApplyTo`).
- `internal/project/` — loads `supacraft.json`, validates it (`validate.go`), compiles it into the tenant config the server reads (`compile.go`: names become ids, `${ENV}` refs resolved from `.env`, auth `users` columns injected) and applies it (`apply.go`: additive SQLite DDL + `.supacraft/tenants/<id>.json`).
- `internal/mcpserver/` — MCP tools. Every write goes through `mutate`: validate → save → apply → reload; invalid changes are not saved.
- `internal/app/` — starts the API server in-process.
- `internal/docs/reference.md` — the config reference served to agents. Keep it in sync with `internal/tenant` models and `internal/pipeline` step types.
- API runtime: `handler`, `middleware`, `pipeline`, `query`, `schema`, `storage`, `tenant`, `cronjob`, `errorlog`.
- `sqlitedb/` and `sqlitedb/ddl` — provisioning and additive migrations. `studio/` — read-only data browser.

## Rules

- Everything is identified by name (tables, columns, functions, cronjobs); compiled UUID fields equal the name.
- Migrations are additive only; never emit DROP/RENAME.
- Quote SQL identifiers with `query.QuoteIdent`, never Go's `%q`. Validate client-supplied column names against the schema before they reach SQL.
- Anything reachable over HTTP is untrusted: no internal error details in responses (`writeErrorJSON` trims them), and new routes go under the tenant group so they get tenant validation, rate limits and the body cap.
- In `supacraft mcp`, stdout is the MCP transport — log to stderr only.
- A new pipeline step type needs: execution in `internal/pipeline/engine.go`, validation in `internal/project/validate.go`, docs in `internal/docs/reference.md`.
