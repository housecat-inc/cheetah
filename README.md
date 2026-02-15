# Spacecat

Spacecat is a utility to manage developing many versions of many apps at once.

## Quick Start

Add a spacecat `main.go` to your app. Now you, your team, and/or your agents can run `go run main.go` to develop many copies of the app at once.

Spacecat coordinates a single multi-tenant HTTP proxy and Postgres database for all environments.

- `SPACE`: friendly name like `little-rock` or derived branch name `nzoschke-add-notes`
- `PORT`: one of two ports to bind to for running the dev app with the "blue / green" pattern
- `DATABASE_TEMPLATE_URL`: database with migrations applied `postgres://localhost:54320/t_abc123`
- `DATABASE_URL`: copy of template database for the space `postgres://localhost:54320/little-rock`

Conventions over configuration:

- code: `$SPACE` or git worktree dir with branch name
- dependency manifest: `go.mod`
- config: `.envrc`
- backing services: multi-tenant postgres, `find sqlc.yaml`, migrate template database once then create `$DATABASE_URL`
- build: watch files; ignore `.gitignore`, `DO NOT EDIT` comment; run `go generate`, `go build cmd/app`
- port: `$PORT` and `$PROXY_PORT` with blue/green router and OAuth bouncer
- disposability: `/health` endpoint
- logs: `slog`
- admin processes: `go test`, `go run ./cmd/app db migrate`

## Development

Run `go run main.go` to run the proxy in development mode with live reload.

```bash
go test -v ./... -artifacts artifacts
```

## Roadmap

- [ ] Internet gateway
- [ ] Remote, shared, encrypted config
