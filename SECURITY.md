# Security Policy

## Supported versions

Security fixes go into the latest release only. Upgrade with
`go install github.com/attisvalentin/supacraft@latest` or download the newest binary from
[Releases](https://github.com/attisvalentin/supacraft/releases).

## Secrets on a central server

- Secret values live only in `<data-dir>/env/<tenant>.env` (directory `0700`, files `0600`).
  Tenant configs and deploys keep `${NAME}` references; values are filled in when the server
  loads a tenant.
- Remote secret management (`/admin/tenants/...`) is off unless `SUPACRAFT_ADMIN_SECRET` is set
  to at least 32 characters. Requests are HMAC-signed with a timestamp and a single-use nonce,
  and the CLI refuses plain `http://` to anything but localhost. Responses list secret names,
  never values.
- `supacraft secrets set` reads the value from a hidden prompt or stdin, never from arguments.
- The MCP `check_secrets` tool returns names only, and the agent instructions forbid asking for
  secret values in chat.

## Reporting a vulnerability

Please **do not** open a public issue. Report it privately through
[GitHub Security Advisories](https://github.com/attisvalentin/supacraft/security/advisories/new).
Include the SupaCraft version, the relevant part of `supacraft.json` and the requests that
trigger the problem.

You should get an answer within a few days. Once a fix is released, the advisory is published
with credit to the reporter, unless you prefer to stay anonymous.
