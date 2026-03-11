# Project Guidelines

## Code Style
- Keep Go code formatted with `gofmt`/`goimports` and target Go 1.23 modules; the repository is purposefully small, so follow the existing single-package layout when adding files.
- Follow the linter.ps1 expectations: avoid decorated comment blocks (`// ===`, `/* ---`) and uppercase-leading comments; `// TODO` and `//go:` directives remain allowed.
- Preference for lowercase, concise inline comments that explain intent, especially around concurrency and stream resolution logic.

## Architecture
- `bedrock_server/main.go` wires the gRPC server (50052) and HTTP proxy (8080) together and registers each provider adapter (`providers/spotify.go`, `providers/soundcloud.go`, etc.).
- The resolver in `bedrock_server/resolver.go` bridges non-streamable Spotify/Deezer tracks by searching SoundCloud and returning a playable URL, so new work should respect that separation of metadata vs. stream discovery.
- Authentication lives in `bedrock_server/auth.go` (JWT issuance, refresh, PostgreSQL-backed users) and the interceptor in `server/middleware/auth_interceptor.go` enforces it via the `authorization: bearer <token>` metadata header.

## Build and Test
- Install deps and run locally with `go mod download` followed by `go run ./bedrock_server` (see README for `.env` variables for Spotify/ SoundCloud credentials). Production builds rely on `docker build -t bedrock-api .` plus `docker run -p 50052:50052 -p 8080:8080 --env-file .env bedrock-api`.
- Linting is enforced via `.\\linter.ps1` (PowerShell); it checks comment patterns and skips generated `*pb.go` files.
- Integration suites live in `tests/`; run `go run ./tests/` (and per-platform entrypoints such as `tests/spotify`, `tests/youtube`, `tests/auth`)

## Conventions
- Provider result IDs follow the `platform:native_id` pattern (e.g., `spotify:4Z8W4fKeB5YxbusRsdQVPb`) so that resolvers can distinguish sources.
- Expect fan-out behavior: searches call all requested providers in parallel and respond with `ResponseStatus_PARTIAL` whenever one or more providers fail — handle partial responses gracefully instead of assuming a total failure.
- Stream metadata flows through the HTTP proxy (`bedrock_server/proxy.go`); modifications to streaming behavior should honor range headers, `io.Copy` buffering, and caching strategy already in place.
- Store infrastructure assumes the simple `db/migrations/000001_create_users_table.{up,down}.sql` schema; keep migrations in sync with the PostgreSQL-backed user store in `store/user_store.go`.
- Refer to the README for installation, environment variables, and architectural diagrams before adding new services or helpers.
