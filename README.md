# Cheetah

When agentic coding a fast dev/test/build/run cycle is a super power for iterating on many ideas at once.

Cheetah provides a drop-in harness for developing many apps / worktrees at once with fast server rebuilds, database provisioning and error feedback.

## Quick Start

Add a `main.go` with `cheetah.Run()` to your app. Now you, your team, and your agents can run `go run main.go` to develop many copies of the app in parallel.

Cheetah coordinates a single multi-tenant HTTP proxy and Postgres database for all environments.

Access your app at `http://localhost:50000` or `https://$SPACE.localhost:50000`. The former serves the latest registered app and serves as convention for OAuth redirects. The latter lets you access multiple apps at the same time.

Cheetah also coordinates app config vars and backing services:

- `SPACE`: unique friendly name, e.g. `little-rock` or worktree branch derived `nzoschke-add-notes`
- `PORT`: one of two ports to bind to for "blue / green deployment" pattern
- `DATABASE_TEMPLATE_URL`: database with migrations pre-applied e.g. `postgres://localhost:54320/t_abc123`
- `DATABASE_URL`: copy of template database for the space, e.g. `postgres://localhost:54320/little-rock`

It will also inject any global app config configured through the dashboard.

Cheetah works with apps that follow twelve-factor conventions:

- code: `$SPACE` and git worktree dir with branch name
- dependency manifest: `go.mod`
- config: `.envrc` and `direnv`
- backing services: multi-tenant postgres; detect `sqlc.yaml schema`, migrate a template database once, then create many `$DATABASE_URL` for dev and test envs
- build: watch files; ignore `.gitignore`, `DO NOT EDIT` comment; run `go generate`, `go build cmd/app`
- port: `$PORT` with blue/green deploys and OAuth bouncer
- disposability: `/health` endpoint
- logs: `slog` with error monitoring
- admin processes: `go test`

## Development

Run `go run main.go` to run cheetah in development mode with live reload. Run `go test -v ./... -artifacts artifacts` to verify.

## Roadmap

- [x] Remote, shared, encrypted config
- [ ] Log collector and error callback to agent
- [ ] Test runner with short, parallel, and error callback optimizations
- [ ] Internet gateway
