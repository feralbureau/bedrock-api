# CLAUDE.md

This file provides guidance to Claude Code when working with this repository.

## Code Style

- Keep Go code formatted with `gofmt`/`goimports`; target **Go 1.23** modules.
- The repository is intentionally small — follow the existing single-package layout when adding files.
- Avoid decorated comment blocks (`// ===`, `/* ---`) and uppercase-leading comments; `// TODO` and `//go:` directives are allowed.
- Prefer lowercase, concise inline comments that explain **intent**, especially around concurrency and stream resolution logic.

## Architecture

- **Entry point:** `bedrock_server/main.go` wires the gRPC server (`:50052`) and HTTP proxy (`:8080`) together and registers each provider adapter (`providers/spotify.go`, `providers/soundcloud.go`, etc.).
- **Resolver:** `bedrock_server/resolver.go` bridges non-streamable Spotify/Deezer tracks by searching SoundCloud and returning a playable URL. Maintain the separation between *metadata* and *stream discovery*.
- **Authentication:** `bedrock_server/auth.go` handles JWT issuance, refresh, and PostgreSQL-backed users. The interceptor at `server/middleware/auth_interceptor.go` enforces auth via the `authorization: bearer <token>` metadata header.
- **HTTP Proxy:** `bedrock_server/proxy.go` handles stream metadata; respect range headers, `io.Copy` buffering, and the existing caching strategy when modifying streaming behavior.

## Build & Run

```powershell
# Install dependencies
go mod download

# Run locally
go run ./bedrock_server

# Production build
docker build -t bedrock-api .
docker run -p 50052:50052 -p 8080:8080 --env-file .env bedrock-api
```

See the README for required `.env` variables (Spotify, SoundCloud credentials, etc.).

## Testing

```powershell
# Run full integration suite
go run ./tests/

# Per-platform entrypoints
go run ./tests/spotify
go run ./tests/youtube
go run ./tests/auth
```

## Linting

Run the PowerShell linter before committing:

```powershell
.\linter.ps1
```

It checks comment patterns and automatically skips generated `*pb.go` files.

## Conventions

- **Provider IDs** follow the `platform:native_id` pattern (e.g., `spotify:4Z8W4fKeB5YxbusRsdQVPb`) so resolvers can distinguish sources.
- **Fan-out behavior:** searches call all requested providers in parallel and may respond with `ResponseStatus_PARTIAL` when one or more providers fail — handle partial responses gracefully rather than treating them as total failures.
- **Database:** migrations live in `db/migrations/000001_create_users_table.{up,down}.sql`. Keep migrations in sync with the PostgreSQL-backed user store in `store/user_store.go`.
- Refer to the README for installation steps, environment variables, and architectural diagrams before adding new services or helpers.
