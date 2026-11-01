# SupaCraft Reference

The authoritative reference for `supacraft.json`. Tables, columns, functions, cronjobs and email templates are identified by **name** — never write UUIDs.

---

## Config Shape (supacraft.json)

```json
{
  "name": "my-app",
  "auth_enabled": true,
  "schema": { "tables": [], "relations": [] },
  "api_settings": { ... },
  "functions": { "<name>": { ... } },
  "cronjobs": { "<name>": { ... } }
}
```

`name` is required; every other section is optional. `auth_enabled` turns on the built-in `users` table and `/auth/*` endpoints — the six auth columns (`id`, `email`, `password`, `is_active`, `created_at`, `updated_at`) are added automatically; declare a `users` table only to add extra columns.

The database is SQLite. Schema changes are **additive**: new tables and new columns are created, but drops, renames and type changes are not applied to existing data.

---

## Schema

Defines database tables, columns, and relationships.

```json
{
  "schema": {
    "tables": [
      {
        "name": "posts",
        "exposed": true,
        "columns": [
          { "name": "id", "type": "uuid", "readable": true, "creatable": false, "updatable": false },
          { "name": "title", "type": "varchar", "length": 255, "nullable": false, "readable": true, "creatable": true, "updatable": true },
          { "name": "body", "type": "text", "nullable": true, "readable": true, "creatable": true, "updatable": true },
          { "name": "status", "type": "enum", "enum_values": ["draft", "published", "archived"], "default_value": "draft", "readable": true, "creatable": true, "updatable": true },
          { "name": "view_count", "type": "int", "default_value": "0", "readable": true, "creatable": false, "updatable": false },
          { "name": "price", "type": "decimal", "precision": 10, "scale": 2, "readable": true, "creatable": true, "updatable": true },
          { "name": "created_at", "type": "timestamptz", "default_value": "NOW()", "readable": true, "creatable": false, "updatable": false }
        ]
      }
    ],
    "relations": [
      { "from": { "table": "comments", "column": "post_id" }, "to": { "table": "posts", "column": "id" }, "type": "one-to-many" }
    ]
  }
}
```

Table names starting with `_supacraft_` are reserved: SupaCraft keeps its own tables there (for example `_supacraft_files`, which records uploads so a purged tenant's files can be deleted from remote storage).

### Valid Column Types

| Type | Stored as (SQLite) | Notes |
|------|-------------------|-------|
| `primary` | `TEXT PRIMARY KEY` | Auto-generated UUID v4 |
| `uuid` | `TEXT` | |
| `int`, `bigint`, `smallint`, `tinyint`, `bigserial` | `INTEGER` | |
| `boolean` | `INTEGER` | 0/1 |
| `decimal`, `numeric`, `real`, `double`, `float` | `REAL` | `precision`/`scale` accepted but not enforced |
| `varchar`, `char`, `text` | `TEXT` | `length` accepted but not enforced |
| `timestamp`, `timestamptz`, `date`, `time`, `interval` | `TEXT` | ISO-8601 strings |
| `json`, `jsonb` | `TEXT` | |
| `blob`, `binary`, `bytea` | `BLOB` | |
| `enum` | `TEXT` | Requires `enum_values`; not enforced by the database — validate in a pipeline `if` step when it matters |

A column named `id` with `"primary": true` (or type `primary`) gets an auto-generated UUID when an insert omits it.

### Column Properties

| Property | Type | Required | Notes |
|----------|------|----------|-------|
| `name` | string | Yes | Max 63 chars |
| `type` | string | Yes | One of the valid types above |
| `nullable` | boolean | No | Default: true |
| `default_value` | string | No | Literal or SQL expression (`NOW()`, `GEN_RANDOM_UUID()`, `CURRENT_TIMESTAMP`) |
| `unique` | boolean | No | Default: false |
| `enum_values` | string[] | If type=enum | 1-100 values, max 63 chars each |
| `length` | integer | No | For varchar/char (1-10000) |
| `precision` | integer | No | For decimal/numeric (1-38) |
| `scale` | integer | No | For decimal/numeric (0-38) |
| `readable` | boolean | No | Column visible in API queries |
| `creatable` | boolean | No | Column settable on create |
| `updatable` | boolean | No | Column settable on update |

### Relation Types

| Type | Meaning |
|------|---------|
| `one-to-one` | Single record relationship |
| `one-to-many` | Parent has many children |
| `many-to-many` | Join table relationship |

---

## api_settings

```json
{
  "api_settings": {
    "timezone": "Europe/Budapest",
    "tables": {
      "posts": {
        "query":  { "enabled": true, "auth": true, "row_policy": "user_id = $auth.id" },
        "create": { "enabled": true, "auth": true },
        "update": { "enabled": true, "auth": true, "row_policy": "user_id = $auth.id" },
        "delete": { "enabled": true, "auth": true, "row_policy": "user_id = $auth.id" }
      }
    },
    "auth_endpoints": { "register": true, "login": true, "refresh": true },
    "crud_endpoints":  { "query": true, "create": true, "update": true, "delete": true },
    "env": {
      "STRIPE_API_KEY": "${STRIPE_API_KEY}",
      "WEBHOOK_SECRET":  "${WEBHOOK_SECRET}"
    },
    "email": {
      "smtp": {
        "enabled": true,
        "provider": "mailpit",
        "host": "localhost",
        "port": 1025,
        "username": "",
        "password": "",
        "encryption": "none",
        "from_email": "no-reply@example.com",
        "from_name": "My App"
      },
      "templates": {
        "welcome": {
          "subject": "Welcome, {{.Name}}!",
          "html": "<h1>Hello {{.Name}}</h1><p>Thanks for joining.</p>",
          "text": "Hello {{.Name}}, thanks for joining."
        }
      }
    },
    "storage": {
      "driver": "local"
    },
    "file_endpoints": {
      "upload":   { "enabled": true, "auth": true },
      "download": { "enabled": true, "auth": false },
      "delete":   { "enabled": true, "auth": true }
    },
    "rate_limit": {
      "enabled": true,
      "requests_per_minute": 100
    },
    "cors": {
      "allowed_origin": "https://app.example.com"
    }
  }
}
```

**`tables`**: Per-table settings for the raw CRUD endpoints, keyed by table **name**.

**`timezone`**: IANA timezone string (e.g. `"America/New_York"`, `"Europe/Budapest"`). Controls:
- `{{now}}` value in pipeline steps
- Cron schedule expression interpretation (e.g. `0 22 * * *` fires at 10pm in this timezone)
- How `timestamp without time zone` DB column values are interpreted in pipeline conditions

Does **not** affect `timestamptz` columns — those are always stored as UTC and need no special handling.
Omit or set `"UTC"` for default UTC behaviour.

**`env`**: Key→value object of variables available as `{{env.KEY}}` in pipeline steps. Keys must match `/^[a-zA-Z_][a-zA-Z0-9_]*$/`. **Never put secret values in supacraft.json** — write a reference `"${KEY}"` and have the user put `KEY=...` in the project's `.env` file (git-ignored). Any string in `api_settings` that is exactly `"${NAME}"` is replaced from `.env` / the process environment when the project is applied. On a central server (`serve --data-dir`) the values live only on the server instead: the user sets them with `supacraft secrets set NAME` (hidden input). Use the MCP `check_secrets` tool to see which names are missing — never ask the user to paste a secret into the chat.

References use the **exact, case-sensitive** key: `{{env.STRIPE_API_KEY}}` resolves the key `STRIPE_API_KEY`. A casing mismatch resolves to empty with no error.

**`email.smtp.encryption`**: `none`, `tls` (STARTTLS, typically port 587), or `ssl` (implicit TLS, typically port 465). The port is read dynamically — match it to the encryption your host allows (e.g. use `587`/`tls` where outbound `465` is blocked).

**`email.smtp.provider`**: free-form label (e.g. `custom`, `resend`, `sendgrid`, `mailgun`, `ses`, `gmail`). For Gmail use `host: smtp.gmail.com`, `port: 587`, `encryption: tls`, `username`: the Gmail address, `password`: a Google **App Password** (requires 2-Step Verification — a normal account password will not work).

**`file_endpoints`**: Per-endpoint `enabled`/`auth` settings for file storage. Downloads are served with `X-Content-Type-Options: nosniff` and a sandboxing `Content-Security-Policy`. Only images (except SVG), audio, video and PDF display in the browser; everything else downloads as an attachment, so uploaded files never run on the API's origin. Three endpoints: `upload`, `download`, `delete`. Each has `enabled` (default `true`) and `auth` (default `false`). Set `auth: true` to require JWT. File upload (`POST /files`) returns `{ "path": "...", "size": ..., "mime": "..." }` — store the `path` in a `varchar` column. File endpoints need a configured `storage` driver (see below) — generate both together.

**`storage`**: Where uploaded files are kept. Shape: `{ "driver": <name>, "config": { ... }, "max_file_size": <bytes>, "allowed_mimes": [<mime>, ...] }`. `config`, `max_file_size`, and `allowed_mimes` are optional. **`local` is the default** — use it whenever the user is undecided; it needs no `config` and stores files on the API server's filesystem under `{STORAGE_PATH}/{tenant-id}/...` (`.supacraft/storage` in the project). When the user wants a cloud/remote backend, set `driver` and the `config` fields below.

| `driver` | required `config` | optional `config` |
|---|---|---|
| `local` | _(none)_ | _(none)_ |
| `s3` | `key`, `secret`, `region`, `bucket` | `endpoint` (for S3-compatible: Cloudflare R2, MinIO, DigitalOcean Spaces) |
| `ftp` | `host`, `username`, `password` | `port` (default `21`), `root`, `ssl` |
| `sftp` | `host`, `username`, `password` | `port` (default `22`), `root` |
| `gcs` | `project_id`, `key_file` (full service-account JSON as a string), `bucket` | _(none)_ |
| `azure` | `name` (account), `key`, `container` | _(none)_ |

`max_file_size` is in **bytes** (the server also clamps to its own `MAX_FILE_SIZE`); omit for the server default. `allowed_mimes` is a string array whitelist (e.g. `["image/jpeg","image/png","application/pdf"]`); omit to allow any type.

**Storage secrets — same rule as `env`.** Write the secret fields (`s3.secret`, `ftp.password`, `sftp.password`, `gcs.key_file`, `azure.key`) as `"${NAME}"` references and put the values in `.env` (on a central server: `supacraft secrets set NAME`, see `env` above).

**`rate_limit`**: Request rate limit per client. `enabled: true` + `requests_per_minute: N` lets each client IP make N requests/minute to the tenant across all endpoints (sliding window), so one noisy client cannot lock out everyone else. Over-limit requests return `429 Too Many Requests`. Omit the block or set `enabled: false` for no limit. Behind a reverse proxy, start the server with `TRUST_PROXY=1` so the limit uses the client address from `X-Real-IP` / `X-Forwarded-For` instead of the proxy's. `/auth/*` is always limited to 20 requests/minute per client IP, whatever this block says.

**`cors`**: Controls which **browser** origins may read responses from the tenant API. Single field `cors.allowed_origin`, a string:
- `"*"` — allow any origin.
- One exact origin `scheme://host[:port]` with **no path** (e.g. `"https://app.example.com"`, `"http://localhost:3000"`). The Go API server sends `Access-Control-Allow-Origin` only when the request's `Origin` matches it exactly.
- Omit the block (or empty string) for no CORS header — browsers on other origins cannot read responses (the default).

A path (`"https://example.com/app"`) or an array is **invalid** — validation rejects it. Only affects browser JS; server-to-server, CLI, and `curl`/Postman ignore CORS. Credentials mode is not used (no `Access-Control-Allow-Credentials`), so authenticate with the `Authorization` Bearer header, not cookies.

**Row policy variables:** `$auth.id` (user UUID from JWT `sub`), `$auth.email`

---

## Function

```json
{
  "functions": {
    "get-users": {
      "name": "get-users",
      "route": {
        "path": "/fn/get-users",
        "method": "GET",
        "auth_required": true,
        "url_params": { "page": "1", "status": "" }
      },
      "steps": [ ... ]
    }
  }
}
```

- The key is the function name; `function.call` refers to it via `function_id`
- `method`: `GET | POST | PUT | PATCH | DELETE | PRIVATE`
- `PRIVATE` functions are only callable via `function.call`, not via HTTP
- `url_params`: optional, GET only. Key = param name, value = default (empty = required). Omit for unrestricted params.

### Path parameters

A whole path segment written as `{name}` matches any non-empty value and is available as `{{params.name}}` (URL-decoded, always a string):

```json
{ "route": { "path": "/fn/posts/{id}/comments", "method": "GET" } }
```

A literal segment beats a parameter, so `/fn/posts/latest` is served by its own function even when `/fn/posts/{id}` exists. Two routes that differ only in parameter names (`/fn/posts/{id}` and `/fn/posts/{slug}`) are the same route and rejected by validation.

### Input validation

`route.input` declares the accepted fields. Requests that don't match get `400 {"error": "invalid input", "fields": {"title": "is required"}}` before any step runs.

```json
"route": {
  "path": "/fn/orders", "method": "POST",
  "input": {
    "product_id": { "type": "string", "format": "uuid", "required": true },
    "qty":        { "type": "integer", "min": 1, "max": 100, "required": true },
    "note":       { "type": "string", "max": 500 },
    "channel":    { "type": "string", "enum": ["web", "app"], "default": "web" }
  }
}
```

- Validates the JSON body (`request.*`) for POST/PUT/PATCH/DELETE and the query string (`query.*`) for GET
- `type`: `string | number | integer | boolean | object | array | any`
- `required`, `default` (filled in when the field is missing)
- `min` / `max`: string length, number value, or array length
- `pattern` (regular expression, strings only), `format`: `email | uuid | date | datetime | url`, `enum`
- **Body:** a field that isn't declared is rejected (`"unknown field"`), so clients can't slip extra columns into a `db.insert` that writes `{{request}}`
- **Query string:** values are converted to the declared type (`?page=2` → number 2, `?tag=a&tag=b` → array); undeclared parameters are dropped. `url_params` defaults still apply
- Prefer `input` over checking fields with `if` steps

---

## Cronjob

```json
{
  "cronjobs": {
    "nightly-cleanup": {
      "name": "nightly-cleanup",
      "schedule": "0 22 * * *",
      "enabled": true,
      "steps": [ ... ]
    }
  }
}
```

The key is the cronjob name.

**Cron examples:**

| Expression | Meaning |
|-----------|---------|
| `0 22 * * *` | Every night at 10 PM |
| `*/15 * * * *` | Every 15 minutes |
| `0 0 1 * *` | First of every month at midnight |
| `0 9 * * 1` | Every Monday at 9 AM |

Cron context: only `env.*` and `now` available. No `auth.*`, `request.*`, `query.*`.

---

## Step Types & Fields

### db.select
```json
{
  "type": "db.select",
  "table": "posts",
  "columns": ["id", "title", "created_at"],
  "where": { "author_id": "{{auth.user_id}}", "status": { "$eq": "published" } },
  "order_by": ["created_at desc", "title"],
  "limit": "{{query.page_size}}",
  "offset": 0,
  "include": [ { "table": "comments", "columns": ["id", "body"] } ],
  "assign": "posts"
}
```
- `columns`: optional, omit for all columns
- `where`: optional filter (same operators as CRUD API)
- `order_by`: `"column"` or `"column desc"`, a string or an array (first sorts first)
- `limit` / `offset`: number or template resolving to a non-negative integer
- `include`: attaches related rows (needs a relation between the tables) under each row's `<table>` key, e.g. `posts[0].comments`. `columns` defaults to the related table's readable columns. If you list `columns` on the step itself, include the link column (e.g. `id` for posts → comments)
- `single: true`: assigns the first row as an object, or `null` when nothing matched (use for "get by id")
- `assign`: stores the result array (or one object/null with `single`)

### db.aggregate
```json
{
  "type": "db.aggregate",
  "table": "orders",
  "where": { "status": "paid" },
  "group_by": ["user_id"],
  "aggregates": { "orders": "count", "revenue": "sum:total", "biggest": "max:total" },
  "order_by": "revenue desc",
  "limit": 10,
  "assign": "top_customers"
}
```
- `aggregates`: alias → `count` | `count:column` | `sum:column` | `avg:column` | `min:column` | `max:column`. `sum` of no rows is `0`
- `group_by`: optional; with it, `assign` gets one row per group (group columns + aliases), without it a single object (`{ "orders": 12, "revenue": 340.5, ... }`)
- `order_by` may use aliases and group columns; `where`, `limit`, `offset` as in db.select

### db.insert
```json
{
  "type": "db.insert",
  "table": "orders",
  "data": { "user_id": "{{auth.user_id}}", "total": "{{request.total}}" },
  "assign": "order"
}
```

### db.update
```json
{
  "type": "db.update",
  "table": "users",
  "where": { "id": "{{auth.user_id}}" },
  "data": { "last_login": "{{now}}" },
  "assign": "updated"
}
```

### db.delete
```json
{
  "type": "db.delete",
  "table": "sessions",
  "where": { "expires_at": { "$lt": "{{threshold}}" } },
  "assign": "result"
}
```
Assigns `{ "deleted": N }`.

### if
```json
{
  "type": "if",
  "condition": "{{user.id}} != null",
  "then": [ { "type": "..." } ],
  "else": [ { "type": "..." } ]
}
```

### foreach
```json
{
  "type": "foreach",
  "items": "{{orders}}",
  "as": "order",
  "steps": [ { "type": "..." } ]
}
```
Max 1000 iterations.

### transaction
```json
{
  "type": "transaction",
  "steps": [
    { "type": "db.insert", "table": "orders", "data": { "product_id": "{{request.product_id}}", "qty": "{{request.qty}}" }, "assign": "order" },
    { "type": "db.select", "table": "products", "where": { "id": "{{request.product_id}}" }, "single": true, "assign": "product" },
    { "type": "if", "condition": "{{product.stock}} < {{request.qty}}", "then": [
      { "type": "fail", "status_code": 409, "message": "out of stock" }
    ] },
    { "type": "transform.math", "expression": "{{product.stock}} - {{request.qty}}", "assign": "left" },
    { "type": "db.update", "table": "products", "where": { "id": "{{request.product_id}}" }, "data": { "stock": "{{left}}" } }
  ]
}
```
- All database steps inside run atomically: if any step fails (including `fail`), every write inside is rolled back. A `try` inside can catch a failure; only that try's writes are undone then
- Variables assigned inside stay available after the transaction
- `http.request`, `email.send` and `response` are not allowed inside (they can't be rolled back); put them after the transaction
- The database is locked for the duration, so keep transactions short
- Nested transactions (also through `function.call`) join the outer one

### fail
```json
{ "type": "fail", "status_code": 403, "message": "you can only edit your own posts", "data": { "post_id": "{{params.id}}" } }
```
- Stops the function and responds with `status_code` (400–599, default 400) and `{ "error": message, ...data }`
- `message` and `data` support templates
- Rolls back an enclosing transaction; a surrounding `try` catches it
- In a cronjob it stops the job and logs the message

### try
```json
{
  "type": "try",
  "as": "err",
  "steps": [
    { "type": "http.request", "url": "https://api.example.com/sync", "method": "POST", "assign": "sync" }
  ],
  "catch": [
    { "type": "set_variable", "assign": "sync", "value": null }
  ]
}
```
- If a step in `steps` fails, runs `catch` (optional) and continues after the try
- Inside a `transaction`, the database writes of a failed `steps` block are undone (the rest of the transaction is kept)
- The error is available as `{{err.message}}`, `{{err.status}}`, `{{err.data}}` (`as` defaults to `error`)
- For `fail` errors, `message`/`status`/`data` are the fail step's. For anything else, `message` is `"internal error"`, `status` is 500, and `{{err.detail}}` has the full error text — don't return `detail` to clients (it can contain SQL or upstream details)
- A `response` inside `steps` is not an error and ends the function as usual

### set_variable
```json
{
  "type": "set_variable",
  "assign": "myVar",
  "value": "hello"
}
```

### transform.sum
```json
{ "type": "transform.sum", "input": "orders.total", "assign": "totalSpent" }
```

### transform.count
```json
{ "type": "transform.count", "input": "orders", "assign": "orderCount" }
```

### transform.map
```json
{ "type": "transform.map", "input": "orders", "field": "id", "assign": "orderIds" }
```
Or multi-field:
```json
{ "type": "transform.map", "input": "orders", "fields": { "id": "id", "amount": "total" }, "assign": "summary" }
```

### transform.filter
```json
{ "type": "transform.filter", "input": "orders", "condition": "{{item.status}} == \"active\"", "assign": "activeOrders" }
```

### transform.math
```json
{ "type": "transform.math", "expression": "{{price}} * {{quantity}} * 1.2", "assign": "totalWithTax" }
```
Operators: `+ - * / %` and `**` (right-associative). Parentheses for grouping. Functions: `abs(x)`, `ceil(x)`, `floor(x)`, `round(x)` (half-away-from-zero), `pow(a, b)`, `min(a, b, ...)`, `max(a, b, ...)`. All math is float64.

Examples: `"ceil({{total}} / {{pageSize}})"`, `"{{n}} % 2"`, `"max({{a}}, {{b}}, 0)"`, `"pow(2, {{exp}})"`, `"({{a}} + {{b}}) / 2"`.

### transform.datetime
```json
{
  "type": "transform.datetime",
  "operation": "subtract",
  "value": 30,
  "unit": "days",
  "input": "{{now}}",
  "assign": "threshold"
}
```
- `operation`: `add | subtract`
- `unit`: `seconds | minutes | hours | days`
- `input`: defaults to `{{now}}` if omitted

### response
```json
{ "type": "response", "status_code": 200, "data": { "user": "{{user}}", "total": "{{totalSpent}}" } }
```
No-op in cronjob context.

### function.call
```json
{
  "type": "function.call",
  "function_id": "calculate_discount",
  "args": { "user_id": "{{user.id}}", "amount": "{{totalSpent}}" },
  "assign": "discount"
}
```
Called function receives args as `{{args.user_id}}`, `{{args.amount}}`. Max recursion depth: 10.

### http.request
```json
{
  "type": "http.request",
  "url": "https://api.stripe.com/v1/charges",
  "method": "POST",
  "headers": { "Authorization": "Bearer {{env.STRIPE_API_KEY}}" },
  "body": { "amount": "{{totalCents}}", "currency": "usd" },
  "assign": "charge"
}
```
`url` must be `http` or `https`. Link-local addresses (including the `169.254.169.254` cloud metadata service) are never reachable. When the URL's host comes from a template (`"https://{{request.host}}/..."`), loopback and private networks are refused too, so request data can't aim the server at your internal network. A fixed host in the URL may be anything, including `localhost`.

### email.send (raw mode)
```json
{
  "type": "email.send",
  "mode": "raw",
  "to": ["{{user.email}}"],
  "subject": "Your order #{{order.id}} is confirmed",
  "html": "<h1>Hi {{user.name}}</h1><p>Order total: {{order.total}}</p>",
  "text": "Hi {{user.name}}, order total: {{order.total}}",
  "reply_to": "support@example.com",
  "assign": "emailResult"
}
```

### email.send (template mode)
```json
{
  "type": "email.send",
  "mode": "template",
  "to": ["{{user.email}}"],
  "template_id": "welcome",
  "data": { "Name": "{{user.name}}" },
  "assign": "emailResult"
}
```
- Template uses Go `html/template` syntax: `{{.Name}}`, `{{.Amount}}`, etc.
- `subject` overrides template subject if set
- Templates defined in `api_settings.email.templates`

**email.send assign result:**
```json
{ "status": "sent", "mode": "raw", "provider": "mailpit", "from": "SupaCraft <no-reply@...>", "to": [...], "accepted": 1 }
```

---

## Pipeline Context

| Key | Available in functions | Available in cronjobs |
|-----|----------------------|----------------------|
| `now` | Yes (in tenant timezone) | Yes (in tenant timezone) |
| `auth.user_id` | Yes (auth_required: true) | No |
| `auth.email` | Yes (auth_required: true) | No |
| `request.*` | Yes POST/PUT/PATCH | No |
| `query.*` | Yes | No |
| `params.*` | Yes (path parameters) | No |
| `env.*` | Yes | Yes |

---

## Timestamp Column Types

Pick the column type by intent (all are stored as ISO-8601 TEXT in SQLite):

| Column type | Stores | Use for |
|-------------|--------|---------|
| `timestamptz` | UTC (converts on read) | Any absolute moment — orders, payments, expiry, bookings |
| `timestamp` | Bare wall-clock, no tz | Recurring times — opening hours, shift start times |
| `date` | Calendar date only | Delivery date, birthday |
| `time` | Time of day only | Daily open/close hours |

**Default recommendation: use `timestamptz` for all event timestamps.**

- `timestamptz` works correctly for single-timezone and multi-timezone apps alike
- `timestamp` + `api_settings.timezone` works only when all users are in the same timezone
- If you use `timestamp` columns, always set `api_settings.timezone` — otherwise pipeline comparisons like `{{item.expires_at}} < {{now}}` will be off by the UTC offset

---

## Variable Syntax Rules

| Pattern | Result |
|---------|--------|
| `"{{user.id}}"` | Resolved value, type-preserved (UUID stays UUID, int stays int) |
| `"Hello {{user.name}}"` | String concatenation |
| `"{{orders.0.total}}"` | Array index 0 |
| `"{{orders.total}}"` | Collect `total` field from all items in array |

---

## Filter Operators (db steps where clause)

| Operator | SQL |
|----------|-----|
| `{ "col": "val" }` | `col = val` |
| `{ "col": { "$eq": "val" } }` | `col = val` |
| `{ "col": { "$ne": "val" } }` | `col != val` |
| `{ "col": { "$gt": 100 } }` | `col > 100` |
| `{ "col": { "$gte": 100 } }` | `col >= 100` |
| `{ "col": { "$lt": 50 } }` | `col < 50` |
| `{ "col": { "$lte": 50 } }` | `col <= 50` |
| `{ "col": { "$in": ["a","b"] } }` | `col IN ('a','b')` |
| `{ "col": { "$like": "%foo%" } }` | `col LIKE '%foo%'` |
| `{ "col": { "$null": true } }` | `col IS NULL` |
| `{ "col": { "$null": false } }` | `col IS NOT NULL` |

---

## Condition Expressions (if / transform.filter)

| Pattern | Meaning |
|---------|---------|
| `{{path}}` | Truthy (not nil, not 0, not "", not false) |
| `{{path}} == "value"` | Equality |
| `{{path}} != "value"` | Inequality |
| `{{path}} > 0` | Numeric comparison |
| `{{path}} == null` | Nil check |
| `{{a}} == {{b}}` | Compare two refs |

---

## Common Examples

### Blog schema with functions
```json
{
  "schema": {
    "tables": [
      {
        "name": "posts",
        "columns": [
          { "name": "id", "type": "uuid", "readable": true, "creatable": false, "updatable": false },
          { "name": "title", "type": "varchar", "length": 255, "readable": true, "creatable": true, "updatable": true },
          { "name": "body", "type": "text", "nullable": true, "readable": true, "creatable": true, "updatable": true },
          { "name": "author_id", "type": "uuid", "readable": true, "creatable": true, "updatable": false },
          { "name": "created_at", "type": "timestamptz", "default_value": "NOW()", "readable": true, "creatable": false, "updatable": false }
        ]
      }
    ]
  },
  "functions": {
    "get-posts": {
      "name": "get-posts",
      "route": { "path": "/fn/get-posts", "method": "GET" },
      "steps": [
        { "type": "db.select", "table": "posts", "assign": "posts" },
        { "type": "response", "data": { "posts": "{{posts}}" } }
      ]
    }
  },
  "api_settings": {
    "tables": {
      "posts": {
        "query": { "enabled": true, "auth": false },
        "create": { "enabled": true, "auth": true },
        "update": { "enabled": true, "auth": true, "row_policy": "author_id = $auth.id" },
        "delete": { "enabled": true, "auth": true, "row_policy": "author_id = $auth.id" }
      }
    }
  }
}
```

### Welcome email on register
```json
{
  "functions": {
    "welcome-email": {
      "name": "welcome-email",
      "route": { "path": "/fn/welcome", "method": "POST", "auth_required": false },
      "steps": [
        { "type": "db.select", "table": "users", "where": { "email": "{{request.email}}" }, "assign": "user" },
        {
          "type": "email.send",
          "mode": "template",
          "to": ["{{user.email}}"],
          "template_id": "welcome",
          "data": { "Name": "{{user.name}}" }
        },
        { "type": "response", "data": { "status": "ok" } }
      ]
    }
  }
}
```

### Nightly cleanup
```json
{
  "cronjobs": {
    "nightly-session-cleanup": {
      "name": "nightly-session-cleanup",
      "schedule": "0 2 * * *",
      "enabled": true,
      "steps": [
        { "type": "transform.datetime", "operation": "subtract", "value": 30, "unit": "days", "assign": "threshold" },
        { "type": "db.delete", "table": "sessions", "where": { "expires_at": { "$lt": "{{threshold}}" } }, "assign": "result" }
      ]
    }
  }
}
```

---

## HTTP API Reference

Use this section when writing scripts, `curl` commands, or client code that talks to the running API (`supacraft dev`, default `http://localhost:8080`).

### Connection Basics

- **Base URL**: `http://localhost:8080` (`--port` on `supacraft dev`/`serve`)
- **Headers:**
  - `Content-Type: application/json`
  - `Authorization: Bearer <jwt>` — only when the endpoint requires auth
  - `X-Tenant-ID` is optional; it defaults to the project

### Important Rules

- **No `/api/` prefix** — all routes are at the root (`/query`, `/auth/login`, `/fn/...`)
- **CRUD endpoints take `table_id` in the request body** — this is the table **name** (e.g. `"table_id": "posts"`)
- **Responses** are always wrapped in `{ "data": ... }` (queries also include `"meta"`)
- **Filter operators** (`$eq`, `$gt`, `$in`, `$like`, `$null`, etc.) work in `where` clauses for both CRUD and pipeline db steps

---

### Auth Endpoints

Available when `auth_enabled: true` on the tenant and the specific endpoint is enabled in `api_settings.auth_endpoints`.

- Passwords need at least `api_settings.auth.password_min_length` characters (default 8, allowed 1–72) and at most 72 bytes.
- Register accepts `email`, `password` and any **creatable** column you added to `users` (e.g. a display name). Columns that grant access (`role`, `is_admin`, `plan`, …) must be `"creatable": false, "updatable": false`, otherwise anyone can register as an admin; validation warns about such columns.
- The built-in columns (`id`, `email`, `password`, `is_active`, `created_at`, `updated_at`) keep their own settings even if you declare them in `users`. `is_active` is set by your own functions or cron jobs, never by the client. A user with `is_active = false` cannot log in or refresh tokens. Access tokens already issued stay valid until they expire (`api_settings.auth.access_token_ttl`, seconds, default 86400).

#### POST /auth/register
```bash
curl -X POST http://localhost:8080/auth/register \
  -H "Content-Type: application/json" \
  -d '{"email": "user@example.com", "password": "SecurePass123!"}'
```
Response: `{ "data": { "id": "...", "email": "...", "created_at": "..." } }`

#### POST /auth/login
```bash
curl -X POST http://localhost:8080/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email": "user@example.com", "password": "SecurePass123!"}'
```
Response: `{ "data": { "token": "eyJ...", "refresh_token": "eyJ...", "expires_in": 86400 } }`

#### POST /auth/refresh
```bash
curl -X POST http://localhost:8080/auth/refresh \
  -H "Content-Type: application/json" \
  -d '{"refresh_token": "eyJ..."}'
```

---

### CRUD Endpoints

Each CRUD endpoint takes `table_id` (the table name) in the JSON body. Enabled/auth controlled per-table via `api_settings.tables.<table>.<method>`.

#### POST /query (read data)
```bash
curl -X POST http://localhost:8080/query \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <jwt>" \
  -d '{
    "table_id": "posts",
    "select": ["id", "name", "email"],
    "where": { "is_active": true },
    "order_by": { "created_at": "desc" },
    "limit": 10,
    "offset": 0
  }'
```
Response:
```json
{
  "data": [{ "id": "...", "name": "...", "email": "..." }],
  "meta": { "total": 42, "limit": 10, "offset": 0, "has_more": true }
}
```

`limit` defaults to 100 and is capped at 100; page with `offset` or `meta.next_cursor`.

**Include related tables**: the included table must have `query.enabled`, and its own `auth` and `row_policy` apply to the included rows. `select` may only name readable columns.
```json
{
  "table_id": "posts",
  "select": ["id", "name"],
  "include": [
    { "table_id": "comments", "select": ["id", "title"] }
  ]
}
```

#### POST /create (insert record)
```bash
curl -X POST http://localhost:8080/create \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <jwt>" \
  -d '{
    "table_id": "posts",
    "data": { "name": "Pikachu", "type": "electric", "power": 55 }
  }'
```
Response: `{ "data": { "id": "...", "name": "Pikachu", "type": "electric", "created_at": "..." } }`

#### POST /update (update records)
```bash
curl -X POST http://localhost:8080/update \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <jwt>" \
  -d '{
    "table_id": "posts",
    "where": { "id": "<record-uuid>" },
    "data": { "name": "Raichu" }
  }'
```
Response: `{ "data": { "id": "...", "name": "Raichu", "updated_at": "..." } }`

#### POST /delete (delete records)
```bash
curl -X POST http://localhost:8080/delete \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <jwt>" \
  -d '{
    "table_id": "posts",
    "where": { "id": "<record-uuid>" }
  }'
```
Response: `{ "data": { "deleted": 1 } }`

---

### Meta Endpoint

#### GET /meta/schema
Returns exposed tables and their readable columns. No auth required.
```bash
curl http://localhost:8080/meta/schema
```
Use this to discover table UUIDs when writing scripts.

---

### Error Log

API error responses carry only a short reason (e.g. `"insert failed"`). The full message, including the database or server error, goes to the error log, which is **not** served over HTTP. Read it with the MCP `get_logs` tool, or open `.supacraft/errors.db` with any SQLite client. Entries are kept for 7 days.

---

### Pipeline Function Endpoints

Route pattern: `/fn/<path>` with the HTTP method defined in the function's `route.method`.

```bash
# GET function with query params
curl "http://localhost:8080/fn/total-spent?page=1" \
  -H "Authorization: Bearer <jwt>"

# POST function with body
curl -X POST http://localhost:8080/fn/create-order \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <jwt>" \
  -d '{"product_id": "...", "quantity": 2}'
```

- `auth_required: true` → must include JWT
- POST/PUT/PATCH body → available as `request.*` in pipeline context
- URL query params → available as `query.*` in pipeline context
- Path parameters (`/fn/posts/{id}`) → available as `params.*`
- `route.input` failures → `400 {"error": "invalid input", "fields": {...}}`
- `fail` steps → their `status_code` with `{"error": "<message>", ...data}`

---

### File Endpoints

File storage endpoints for uploading, downloading, and deleting files. The storage backend is set via `api_settings.storage` (see the **`storage`** subsection under api_settings) — `local` is the default. Enabled/auth controlled via `api_settings.file_endpoints`.

#### POST /files (upload)
```bash
curl -X POST http://localhost:8080/files \
  -H "Authorization: Bearer <jwt>" \
  -F "file=@photo.jpg" \
  -F "folder=avatars"
```
- `file`: required, multipart file field
- `folder`: optional, organizes files into subfolders (alphanumeric, hyphens, underscores)

Response:
```json
{ "data": { "path": "avatars/a1b2c3d4e5f6.jpg", "size": 245760, "mime": "image/jpeg" } }
```

Store the returned `path` in a `varchar` column — that's all you need to reference the file later.

#### GET /files/{path} (download)
```bash
curl http://localhost:8080/files/avatars/a1b2c3d4e5f6.jpg
```
Streams the file with auto-detected `Content-Type` header.

#### DELETE /files/{path} (delete)
```bash
curl -X DELETE http://localhost:8080/files/avatars/a1b2c3d4e5f6.jpg \
  -H "Authorization: Bearer <jwt>"
```
Response: `{ "data": { "deleted": true } }`

### Working with Files in Pipelines

- File upload is HTTP-only (not a pipeline step) — the client uploads first, gets back a `path`
- Store the path in a `varchar` column via `db.insert` or `db.update`
- Reference the stored path in responses or pipeline logic like any other column value
- To build a "profile picture upload" flow: client uploads via `POST /files`, then calls a pipeline function with the returned `path` to save it to the user's record

---

### Scripting Workflow (e.g. Seeding Data)

When writing scripts to seed or test the API:

```bash
# 1. Register a user (if auth is needed)
curl -X POST http://localhost:8080/auth/register \
  -H "Content-Type: application/json" \
  -d '{"email": "seed@test.com", "password": "Pass123!"}'

# 2. Login to get JWT
TOKEN=$(curl -s -X POST http://localhost:8080/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email": "seed@test.com", "password": "Pass123!"}' | jq -r '.data.token')

# 3. Create records (table_id is the table name)
curl -X POST http://localhost:8080/create \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"table_id": "posts", "data": {"name": "Thunderbolt", "type": "electric"}}'

# 4. Verify
curl -X POST http://localhost:8080/query \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"table_id": "posts", "limit": 5}'

# 5. Debug errors: use the MCP get_logs tool (or open .supacraft/errors.db)
```
