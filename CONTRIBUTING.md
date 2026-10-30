# Contributing to SupaCraft

Thanks for your interest! Bug reports, ideas and pull requests are all welcome.

## Before you start

- For anything bigger than a small fix, **open an issue first** so we can agree on the approach
  before you spend time on it.
- Keep pull requests focused: one change per PR is much easier to review.

## Development

You need Go 1.26 or newer.

```bash
git clone https://github.com/attisvalentin/supacraft
cd supacraft
go build -o supacraft .
go test ./...
go vet ./...
```

Try your change against a real project:

```bash
mkdir /tmp/demo && ./supacraft init -C /tmp/demo
./supacraft dev -C /tmp/demo
```

[CLAUDE.md](CLAUDE.md) gives a short map of the codebase.

## Ground rules

- **Migrations are additive.** Never generate `DROP` or `RENAME`; users' data must be safe.
- **Everything is addressed by name** in `supacraft.json`, never by generated IDs.
- **New pipeline step types** need three things: execution in `internal/pipeline`, validation in
  `internal/project/validate.go` (with a JSON path in every error) and documentation in
  `internal/docs/reference.md`, which is what AI agents read.
- **`supacraft mcp` owns stdout.** Log to stderr only.
- Add tests for new behaviour.

## How pull requests are merged

1. Open a PR against `main`. Direct pushes to `main` are blocked.
2. [CodeRabbit](https://coderabbit.ai) reviews it automatically. Please address or reply to its
   comments.
3. CI (`go vet`, `go test`) must pass.
4. A maintainer reviews it. Merging needs the maintainer's approval, and all review
   conversations must be resolved.

## License

By contributing you agree that your contributions are licensed under the [MIT License](LICENSE).
