<div align="center">

# SupaCraft

**Describe a backend in plain English. Get a running REST API.**

SupaCraft is an open-source backend that an AI agent builds for you: database, user accounts,
API endpoints, file uploads, emails and scheduled jobs, all defined in one JSON file and
served by one small binary.

[![CI](https://github.com/attisvalentin/supacraft/actions/workflows/ci.yml/badge.svg)](https://github.com/attisvalentin/supacraft/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

</div>

---

## What is this?

Most apps need the same backend: a database, sign-up and login, a few endpoints that read and
write data, maybe file uploads, an email now and then, a nightly cleanup job. Writing it by hand
takes days. Firebase and Supabase are quicker, but you then depend on a hosted platform and its
dashboard.

SupaCraft goes another way:

1. **The whole backend is one file, `supacraft.json`.** Tables, permissions, endpoints, cron
   jobs: everything is declarative JSON that lives in your git repo.
2. **An AI agent writes that file for you.** SupaCraft ships an [MCP](https://modelcontextprotocol.io)
   server, so Claude Code, Cursor or any MCP-capable agent can design the schema, add endpoints
   and test them, while SupaCraft checks every change.
3. **One binary runs it.** `supacraft dev` turns the file into a live API backed by SQLite.
   You don't need Docker, a cloud account or a separate database server. It runs the same on
   your laptop and on a $5 VPS.
4. **One server runs all your APIs.** Start `supacraft serve --data-dir` once on your server.
   Every new backend is one `supacraft deploy` away: the server creates its database and serves
   it within seconds, next to the others, without a restart or a new process. The
   `X-Tenant-ID` header picks which API answers a request.

It is built for **lightning-fast MVPs, prototypes, internal tools and side projects**, where you
want a real backend in minutes rather than days and you don't want to run anyone's platform.

### What you get

| | |
|---|---|
| 🗄️ **Database** | SQLite tables generated from your schema, migrated automatically when you add tables or columns |
| 🔐 **Auth** | `/auth/register`, `/auth/login`, `/auth/refresh` with JWT; per-endpoint auth and row-level policies |
| ⚡ **Custom endpoints** | `/fn/posts/{id}` routes built from steps: queries with joins and aggregates, transactions, input validation, if/else, loops, try/catch, math, HTTP calls, emails |
| ⏰ **Cron jobs** | The same steps on a schedule (`0 3 * * *`) |
| 📁 **File storage** | Upload/download endpoints backed by local disk, S3/R2/MinIO, GCS, Azure, FTP or SFTP |
| ✉️ **Email** | SMTP with templates |
| 🤖 **AI-native** | Built-in MCP server: the agent edits, validates, migrates, calls endpoints and queries data |
| 🔍 **Studio** | Read-only web UI to browse your data |
| 🏢 **Many APIs, one server** | One `serve` process hosts every project; `supacraft deploy` adds or updates one live; `${NAME}` secrets stay on the server |

## How it feels

```text
you  › Build me a blog backend: posts with comments, anyone can read,
       only logged-in users can write, and email the author on each new comment.

agent › set_table posts ✓   set_table comments ✓   set_relations ✓
        set_function list_posts ✓   set_function create_post ✓
        set_function add_comment ✗ steps[2].template_id: unknown email template "new_comment"
        update_api_settings (email template new_comment) ✓
        set_function add_comment ✓
        call_endpoint POST /auth/register → 201
        call_endpoint POST /fn/posts → 201
        call_endpoint GET  /fn/posts → 200 [1 post]

      Done. Your API is running on http://localhost:8080.
```

The agent never hand-edits files blindly: every change is **validated before it's saved**.
If something is wrong, it gets an exact error such as
`functions.add_comment.steps[2].template_id: unknown email template`, fixes it and tries again.
Your `supacraft.json` is never left in a broken state.

## Quick start

**Install** (Go 1.26+, or grab a binary from [Releases](https://github.com/attisvalentin/supacraft/releases)):

```bash
go install github.com/attisvalentin/supacraft@latest
```

**Create a project:**

```bash
mkdir my-app && cd my-app
supacraft init
```

This creates:

```text
my-app/
├── supacraft.json   ← your entire backend
├── .env             ← secrets (git-ignored)
├── .gitignore
└── .mcp.json        ← registers the SupaCraft MCP server for your AI agent
```

**Build it with AI.** Open the folder in Claude Code (or any MCP client that reads `.mcp.json`)
and describe what you want. The agent starts `supacraft mcp`, which also serves the API.

**…or by hand.** Edit `supacraft.json` and run:

```bash
supacraft dev    # API on http://localhost:8080, re-applied on every save
```

**Ship it.** On your server, start one SupaCraft for all your projects, once. Run it as
your normal (non-root) user; the installer asks for `sudo` itself where it needs it:

```bash
sudo mkdir -p /var/lib/supacraft && sudo chown "$USER" /var/lib/supacraft
umask 077 && echo "SUPACRAFT_ADMIN_SECRET=$(openssl rand -hex 32)" > /var/lib/supacraft/.env
supacraft service install --data-dir /var/lib/supacraft
```

Then deploy each project from your machine; no new process, no restart. Put the same
`SUPACRAFT_ADMIN_SECRET` in your local `.env`. For a project that defines `GET /fn/posts`
(like the [complete example](#a-complete-example) below):

```bash
supacraft deploy --server https://api.example.com
curl -H "X-Tenant-ID: my_app" https://api.example.com/fn/posts
```

See [Many APIs on one server](#many-apis-on-one-server) for secrets and tenant management.

## A complete example

This file is a working blog API with users, posts and two endpoints:

```json
{
  "name": "blog",
  "auth_enabled": true,
  "schema": {
    "tables": [
      {
        "name": "posts",
        "columns": [
          { "name": "id", "type": "uuid", "primary": true },
          { "name": "title", "type": "varchar", "nullable": false },
          { "name": "author_id", "type": "uuid" },
          { "name": "created_at", "type": "timestamptz", "default_value": "NOW()" }
        ]
      }
    ]
  },
  "functions": {
    "list_posts": {
      "route": { "path": "/fn/posts", "method": "GET" },
      "steps": [
        { "type": "db.select", "table": "posts", "order_by": "created_at desc", "limit": 20, "assign": "posts" },
        { "type": "response", "status_code": 200, "data": { "posts": "{{posts}}" } }
      ]
    },
    "get_post": {
      "route": { "path": "/fn/posts/{id}", "method": "GET" },
      "steps": [
        { "type": "db.select", "table": "posts", "where": { "id": "{{params.id}}" }, "single": true, "assign": "post" },
        { "type": "if", "condition": "{{post}} == null", "then": [
          { "type": "fail", "status_code": 404, "message": "post not found" }
        ] },
        { "type": "response", "status_code": 200, "data": { "post": "{{post}}" } }
      ]
    },
    "create_post": {
      "route": {
        "path": "/fn/posts", "method": "POST", "auth_required": true,
        "input": { "title": { "type": "string", "required": true, "min": 1, "max": 200 } }
      },
      "steps": [
        {
          "type": "db.insert", "table": "posts", "assign": "post",
          "data": { "title": "{{request.title}}", "author_id": "{{auth.user_id}}" }
        },
        { "type": "response", "status_code": 201, "data": { "post": "{{post}}" } }
      ]
    }
  }
}
```

Try it:

```bash
curl -X POST localhost:8080/auth/register -d '{"email":"a@b.co","password":"Secret123!"}'
TOKEN=$(curl -s -X POST localhost:8080/auth/login \
  -d '{"email":"a@b.co","password":"Secret123!"}' | jq -r .data.token)

curl -X POST localhost:8080/fn/posts -H "Authorization: Bearer $TOKEN" -d '{"title":"Hello"}'
curl localhost:8080/fn/posts
```

The `users` table and the auth endpoints come from `"auth_enabled": true`; you don't declare them.

## How it works

```text
             ┌──────────────────────┐
 AI agent ──►│ supacraft mcp        │   validate → save → migrate → reload
   (MCP)     └──────────┬───────────┘
                        ▼
 you ──edit──►  supacraft.json  ──► compile ──► .supacraft/tenants/<app>.json ──► REST API
                        │                                                         (:8080)
                        └──────► additive migration ──► .supacraft/<app>.db (SQLite)
```

- **`supacraft.json`** is the only source of truth. Everything is referenced by name (tables,
  columns, functions), never by generated IDs, so the file stays readable and diff-friendly.
- **Validation** checks column types, that referenced tables and columns exist, step types and
  their required fields, function calls, cron expressions, math expressions and more.
- **Migrations are additive.** New tables and columns are created automatically. SupaCraft never
  drops or renames anything in your data.
- **Runtime state** (the SQLite database, uploaded files and the compiled config) lives in
  `.supacraft/`, which is git-ignored.

## Commands

| Command | Description |
|---|---|
| `supacraft init [name]` | Create a new project (`--auth=false` to skip built-in users) |
| `supacraft dev` | Run the API and re-apply `supacraft.json` / `.env` on every change |
| `supacraft serve` | Apply once and run the API (production); `--data-dir` serves every tenant of a central server |
| `supacraft mcp` | MCP server for AI agents on stdio; also serves the API |
| `supacraft validate` | Check `supacraft.json` and print errors and warnings |
| `supacraft apply` | Migrate the database and write the compiled config (`--data-dir` = into a central server) |
| `supacraft tenants` | `list`, `disable`, `enable`, `remove [--purge]` tenants of a central server and show their `errors`, from your machine (`--server`) or on the server (`--data-dir`) |
| `supacraft deploy` | Send `supacraft.json` to a central server: validate, migrate, go live (`--dry-run` first). Values from `.env` are never sent; write secrets as `${NAME}` references, since literal values in `supacraft.json` are sent |
| `supacraft secrets` | `set`, `unset`, `list` a tenant's secrets on a central server (values never leave the server) |
| `supacraft studio` | Open a read-only data browser |
| `supacraft service install` | Install `serve` as a systemd service (Linux); `--data-dir` for a central server |

Every command accepts `--dir` / `-C` to point at a project directory.

`dev`, `mcp` and `studio` listen on `127.0.0.1` only, so your work-in-progress API is not reachable
from the network. Pass `--host 0.0.0.0` to expose it (for example to test from a phone).
`serve` listens on all interfaces.

## MCP tools

| Tool | Purpose |
|---|---|
| `get_config`, `get_reference` | Read the project and the full format reference |
| `set_table`, `delete_table`, `set_relations` | Shape the database |
| `set_function`, `delete_function` | Custom `/fn/...` endpoints |
| `set_cronjob`, `delete_cronjob` | Scheduled jobs |
| `update_api_settings`, `set_auth` | Permissions, CORS, rate limits, email, storage, env |
| `validate`, `apply` | Check, or apply hand-made edits |
| `call_endpoint` | Send a real HTTP request to the running API |
| `query_db` | Read-only SQL against the database (the JWT key and password hashes are not readable) |
| `get_logs` | Recent server errors (5xx) of the local API |
| `get_server_errors`, `list_tenants` | Errors of this project on the central server (secret values masked), and every API on it |
| `check_secrets` | Which `${NAME}` secrets are missing (names only), and the command the user runs to set them |

The full `supacraft.json` reference is [internal/docs/reference.md](internal/docs/reference.md).
Agents get it as the `supacraft://reference` resource.

## Secrets

Secrets never go into `supacraft.json`. Reference them as `"${NAME}"` and put the value in `.env`
or the process environment:

```json
"api_settings": {
  "env":   { "STRIPE_KEY": "${STRIPE_KEY}" },
  "email": { "smtp": { "host": "smtp.resend.com", "password": "${SMTP_PASSWORD}" } }
}
```

```bash
# .env
STRIPE_KEY=sk_live_...
SMTP_PASSWORD=...
```

On a central server, secrets are kept **only on the server** (`<data-dir>/env/<tenant>.json`, mode
`0600`). Deploys and compiled configs carry just the `${NAME}` references; the server fills them in
when it loads a tenant, so a new value takes effect within seconds. You set them from your
machine, typing the value into a hidden prompt:

```bash
# in the project's .env (not secret values, just how to reach the server)
SUPACRAFT_SERVER=https://api.example.com
SUPACRAFT_ADMIN_SECRET=...        # same value as on the server

supacraft secrets list            # names only: which are set, which are missing
supacraft secrets set STRIPE_KEY  # Value for STRIPE_KEY (input hidden): ••••
supacraft deploy                  # refuses, listing the names, while a secret is missing
```

The AI agent never sees the values: its `check_secrets` tool reports names and gives you the
command to run.

## Deploying

### Many APIs on one server

One `supacraft serve` process can run every API you have. Each project is a **tenant** in a
central data directory, and the `X-Tenant-ID` header of each request picks the API:

```bash
# on the server, once, as a non-root user that owns the directory
# (the installer uses sudo itself; under sudo the service would run as root).
# Deploys and secrets from your machine need SUPACRAFT_ADMIN_SECRET in
# /var/lib/supacraft/.env (chmod 600):  SUPACRAFT_ADMIN_SECRET=<openssl rand -hex 32>
supacraft service install --data-dir /var/lib/supacraft

# from your machine: add or update an API — the server validates it, creates or migrates
# its database and serves it within seconds, without a restart
cd shop && supacraft deploy       # --dry-run shows the migration first

# or on the server itself
supacraft apply -C ./shop --data-dir /var/lib/supacraft

curl -H "X-Tenant-ID: shop" https://api.example.com/fn/products
```

```text
/var/lib/supacraft/
├── tenants/shop.json   ← compiled config of each tenant
├── tenants/blog.json
├── shop.db             ← one SQLite database per tenant
├── blog.db
├── env/shop.json       ← server-side secrets of each tenant (0600)
├── storage/<tenant>/   ← uploaded files
└── errors.db
```

The tenant id comes from the project name (`"name": "My Shop"` → `my_shop`). Manage tenants from
your machine (or on the server with `--data-dir` instead of `--server`):

```bash
supacraft tenants list --server https://api.example.com
supacraft tenants errors shop        # recent 5xx errors with the internal message, secrets masked
supacraft tenants disable shop       # 503 for every request, cron jobs pause; enable brings it back
supacraft tenants remove shop        # stop serving it; the database stays
supacraft tenants remove shop --purge
```

Deploying a disabled tenant doesn't re-enable it. `--purge` deletes the database, secrets and
uploads, including those in S3 or another remote storage driver: SupaCraft records every upload
and deletes exactly those, never anything else in the bucket. If a remote file can't be deleted, nothing else is, the tenant stays disabled
and you can run it again. Pass `--default-tenant <id>` to `serve` if requests without the header
should go to one tenant.

Back up the whole data directory, for example with [Litestream](https://litestream.io).

### One project

SupaCraft is a single static binary. Copy your project folder to a server and run:

```bash
supacraft serve --port 8080            # or:
supacraft service install              # systemd, restarts on failure (not via sudo)
```

Put a reverse proxy (Caddy, nginx) in front for HTTPS. In that setup, run with `--host 127.0.0.1`
so only the proxy can reach the API, and set `TRUST_PROXY=1` so rate limits see real client
addresses. To back up the app, back up `.supacraft/`
(for example with [Litestream](https://litestream.io) for continuous SQLite replication).

### Server settings

| Variable | Default | Purpose |
|---|---|---|
| `HOST` / `PORT` | see above / `8080` | Listen address (same as `--host` / `--port`) |
| `TRUST_PROXY` | off | Read client IPs from `X-Real-IP` / `X-Forwarded-For` (only behind a proxy) |
| `MAX_BODY_SIZE` | `1048576` | Max JSON request body in bytes |
| `MAX_FILE_SIZE` | `104857600` | Max upload size in bytes |
| `STUDIO_SECRET` | random | Studio password |
| `SUPACRAFT_DATA_DIR` | — | Central data directory (same as `--data-dir`) |
| `SUPACRAFT_ADMIN_SECRET` | off | Signs remote management (deploys, secrets) of a central server; at least 32 characters (`openssl rand -hex 32`). Admin endpoints are off without it |
| `SUPACRAFT_SERVER` | — | On your machine: URL of the central server for `supacraft deploy` and `supacraft secrets` |

## Security

Found a vulnerability? Please report it privately through
[GitHub Security Advisories](https://github.com/attisvalentin/supacraft/security/advisories/new)
rather than in a public issue.

## Is it for me?

**Good fit:** MVPs, hackathons, prototypes, internal tools, side projects, mobile or SPA
backends, and anything where "working today" beats "infinitely flexible". Also for running
**many small backends on one cheap server**: an agency or a maker with ten side projects hosts
them all with one process.

**Not (yet) a fit:** large multi-node deployments (SQLite, one process per server), apps that need
destructive schema migrations, or complex business logic that really wants a general-purpose
language.

## Roadmap ideas

These are open for discussion, and PRs are welcome:

- OpenAPI export so agents and frontends can generate typed clients
- Destructive migrations (rename/drop) with explicit confirmation
- Optional visual schema viewer in Studio
- PostgreSQL as an alternative storage driver

## Contributing

Issues and pull requests are welcome. Please read [CONTRIBUTING.md](CONTRIBUTING.md) first.
Every PR gets an automated review from [CodeRabbit](https://coderabbit.ai) and must pass CI and
a maintainer review before it can be merged.

## License

[MIT](LICENSE)
