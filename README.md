<div align="center">

![Banner](./assets/banner.png)

# Bedrock-API

### your unified gateway to the world's music.

[![Go Version](https://img.shields.io/github/go-mod/go-version/feralbureau/bedrock-api?style=for-the-badge&logo=go&color=00ADD8)](https://go.dev/)
[![License](https://img.shields.io/github/license/feralbureau/bedrock-api?style=for-the-badge&color=yellow)](LICENSE)
[![Stars](https://img.shields.io/github/stars/feralbureau/bedrock-api?style=for-the-badge&color=blue)](https://github.com/feralbureau/bedrock-api/stargazers)
[![Repo Size](https://img.shields.io/github/repo-size/feralbureau/bedrock-api?style=for-the-badge&color=brightgreen)](https://github.com/feralbureau/bedrock-api)

---

</div>

## What is Bedrock?

**Bedrock-API** is the engine under the hood of the Bedrock streaming app. It's a high-performance gRPC server that talks to multiple music platforms at once, so you don't have to.

Instead of juggling different APIs for Spotify, SoundCloud, and Deezer, Bedrock gives you one clean, normalized interface. It handles the heavy lifting parallel searches, metadata normalization, and even "bridging" non-streamable tracks by finding playable alternatives on other platforms automatically.

It includes a built-in **HTTP Streaming Proxy** that handles seeking and caching, so you can use stream/cover urls regardless of the country you live in.

## Key Features

- **Multi-Provider Aggregation**: Search and fetch tracks, albums, artists, and playlists from Spotify, SoundCloud, Deezer, and more.
- **Cross-Platform Bridge**: Automatically resolve non-streamable tracks (Spotify/Deezer) to playable SoundCloud streams.
- **Built-in Streaming Proxy**: High-performance HTTP proxy with support for range requests and true `io.Copy` streaming.
- **Lyrics Integration**: Synced and plain-text lyrics via LrcLib (Genius support in progress).
- **Concurrent Performance**: Fans out requests to providers in parallel using Go's high-concurrency primitives.
- **Normalized Schema**: Unified gRPC/Protobuf models for all music entities, regardless of source.

## Tech Stack

<div>

![Go](https://img.shields.io/badge/go-%2300ADD8.svg?style=for-the-badge&logo=go&logoColor=white)
![gRPC](https://img.shields.io/badge/gRPC-%234285F4.svg?style=for-the-badge&logo=grpc&logoColor=white)
![Protocol Buffers](https://img.shields.io/badge/Protobuf-%23E3E4E2.svg?style=for-the-badge&logo=protocol-buffers&logoColor=black)
![PostgreSQL](https://img.shields.io/badge/PostgreSQL-%23316192.svg?style=for-the-badge&logo=postgresql&logoColor=white)
![JWT](https://img.shields.io/badge/JWT-%23000000.svg?style=for-the-badge&logo=json-web-tokens&logoColor=white)
![Docker](https://img.shields.io/badge/docker-%230db7ed.svg?style=for-the-badge&logo=docker&logoColor=white)

</div>

## Installation

### Prerequisites

- [Go 1.24+](https://go.dev/dl/)
- [PostgreSQL 13+](https://www.postgresql.org/download/)
- [Docker](https://www.docker.com/get-started) (optional)
- Git (for submodule initialization)

### Environment Setup

Create a `.env` file in the root directory and add your provider credentials:

```env
# Database (REQUIRED)
DATABASE_URL=postgres://user:password@localhost:5432/bedrock

# JWT (REQUIRED)
JWT_SECRET=your_secret_key_here

# Spotify (needed for metadata)
SPOTIFY_CLIENT_ID=your_id
SPOTIFY_CLIENT_SECRET=your_secret

# SoundCloud (comma-separated list of client IDs for rotation)
SOUNDCLOUD_CLIENT_IDS=id1,id2

# YouTube (optional, for age-restricted content)
YOUTUBE_COOKIES=your_cookies_here
```

### Running Locally

```bash
# Initialize submodules (if cloning for the first time)
git submodule update --init --recursive

# Install dependencies
go mod download

# Set up database (run migrations)
# Make sure DATABASE_URL is set in your .env file

# Run the server
go run ./bedrock_server
```

The gRPC server will be available at `localhost:50052` and the HTTP proxy at `localhost:8080`.

### Running with Docker

```bash
# Build the image
docker build -t bedrock-api .

# Run the container
docker run -p 50052:50052 -p 8080:8080 --env-file .env bedrock-api
```

## Architecture

Bedrock is designed to be highly modular. Each provider (Spotify, SoundCloud, YouTube, etc.) is implemented as a separate adapter that satisfies a common interface. The **Resolver** automatically bridges non-streamable tracks (from Spotify, Deezer, YouTube) to playable SoundCloud streams.

```mermaid
graph TD
    Client[gRPC Client] --> Server[Bedrock gRPC Server]
    Server --> Resolver[Stream Resolver]
    Server --> Spotify[Spotify Provider]
    Server --> SoundCloud[SoundCloud Provider]
    Server --> Deezer[Deezer Provider]
    Server --> YouTube[YouTube Provider]
    Resolver --> SoundCloud
    Proxy[HTTP Proxy :8080] --> Resolver
```

### Stream Resolution

Bedrock automatically handles non-streamable tracks through its resolver:
- **Spotify tracks**: Resolved to playable SoundCloud URLs when direct streaming is unavailable
- **Deezer tracks**: Similarly bridged to SoundCloud
- **YouTube Music**: Resolved via Innertube; optionally bridges to SoundCloud for broader compatibility

## Supported Providers & Features

| Provider | Metadata | Search | Streaming | Playlist | Bridge Support |
|----------|----------|--------|-----------|----------|----------------|
| Spotify | ✓ | ✓ | ✗ (bridged) | ✓ | SoundCloud |
| SoundCloud | ✓ | ✓ | ✓ | ✓ | — |
| Deezer | ✓ | ✓ | ✗ (bridged) | ✓ | SoundCloud |
| YouTube Music | ✓ | ✓ | ◐ (limited) | ✓ | SoundCloud |
| Yandex | ◐ | ◐ | — | — | — |
| VK | ◐ | ◐ | — | — | — |

✓ = Fully supported | ◐ = Partial/Limited | ✗ = Not supported | — = Not implemented

## Database & Authentication

Bedrock uses **PostgreSQL** for persistent user storage and **JWT** tokens for authentication:

- **User Registration & Login**: gRPC methods handle secure account creation and authentication
- **JWT Tokens**:
  - Access tokens: 15 minutes (for API requests)
  - Refresh tokens: 7 days (for obtaining new access tokens)
- **Password Security**: Passwords are hashed using bcrypt (`golang.org/x/crypto/bcrypt`)
- **Enforcement**: gRPC interceptor (`server/middleware/auth_interceptor.go`) enforces JWT via `authorization: bearer <token>` header
- **Database**: Migrations in `db/migrations/` initialize the user schema. Use your `DATABASE_URL` to connect.

## Managing Submodules

Bedrock includes the **spotify-wrapper** as a git submodule — a local replacement for the public `github.com/spotapi/spotapi-go` module. This allows us to maintain a custom version of the Spotify client.

### Initialization

After cloning the repository, initialize submodules:

```bash
git submodule update --init --recursive
```

### Updating

To pull the latest changes from the submodule:

```bash
git submodule update --recursive --remote
```

After updating the submodule, test the changes:

```bash
go run ./tests/spotify
```

### Module Resolution

`go.mod` includes a local replace directive:

```go
replace github.com/spotapi/spotapi-go => ./spotify-wrapper
```

This tells Go to use the local `./spotify-wrapper` directory instead of the remote module.

## Testing

Run integration tests to verify provider functionality:

```bash
# Full integration test suite
go test ./tests/...

# Per-platform tests
go test ./tests/spotify
go test ./tests/youtube
go test ./tests/auth
go test ./tests/soundcloud
go test ./tests/deezer
go test ./tests/lyrics
```

### Code Quality

Check for common issues with `go vet` before committing:

```bash
go vet ./...
```

## License

Distributed under the MIT License. See `LICENSE` for more information.
