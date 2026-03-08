<div align="center">

![Banner](./assets/banner.png)

# Bedrock-API

### your unified gateway to the world's music.

[![Go Version](https://img.shields.io/github/go-mod/go-version/OWNER/REPO?style=for-the-badge&logo=go&color=00ADD8)](https://go.dev/)
[![License](https://img.shields.io/github/license/OWNER/REPO?style=for-the-badge&color=yellow)](LICENSE)
[![Stars](https://img.shields.io/github/stars/OWNER/REPO?style=for-the-badge&color=blue)](https://github.com/OWNER/REPO/stargazers)
[![Repo Size](https://img.shields.io/github/repo-size/OWNER/REPO?style=for-the-badge&color=brightgreen)](https://github.com/jumpfool/bedrock-api)

[Documentation](#documentation) • [Installation](#installation) • [Telegram Channel](https://t.me/bedrock_app)

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
- **Lyrics Integration**: Synced and plain-text lyrics via LrcLib and Genius.
- **Concurrent Performance**: Fans out requests to providers in parallel using Go's high-concurrency primitives.
- **Normalized Schema**: Unified gRPC/Protobuf models for all music entities, regardless of source.

## Tech Stack

<div align="center">

![Go](https://img.shields.io/badge/go-%2300ADD8.svg?style=for-the-badge&logo=go&logoColor=white)
![gRPC](https://img.shields.io/badge/gRPC-%234285F4.svg?style=for-the-badge&logo=grpc&logoColor=white)
![Protocol Buffers](https://img.shields.io/badge/Protobuf-%23E3E4E2.svg?style=for-the-badge&logo=protocol-buffers&logoColor=black)
![Docker](https://img.shields.io/badge/docker-%230db7ed.svg?style=for-the-badge&logo=docker&logoColor=white)

</div>

## Installation

### Prerequisites

- [Go 1.23+](https://go.dev/dl/)
- [Docker](https://www.docker.com/get-started) (optional)

### Environment Setup

Create a `.env` file in the root directory and add your provider credentials:

```env
# Spotify (needed for metadata)
SPOTIFY_CLIENT_ID=your_id
SPOTIFY_CLIENT_SECRET=your_secret

# SoundCloud (comma-separated list of client IDs for rotation)
SOUNDCLOUD_CLIENT_IDS=id1,id2
```

### Running Locally

```bash
# Install dependencies
go mod download

# Run the server
go run ./bedrock_server
```

### Running with Docker

```bash
# Build the image
docker build -t bedrock-api .

# Run the container
docker run -p 50052:50052 -p 8080:8080 --env-file .env bedrock-api
```

## Architecture

Bedrock is designed to be highly modular. Each provider (Spotify, SoundCloud, etc.) is implemented as a separate adapter that satisfies a common interface.

```mermaid
graph TD
    Client[gRPC Client] --> Server[Bedrock gRPC Server]
    Server --> Resolver[Stream Resolver]
    Server --> Spotify[Spotify Provider]
    Server --> SoundCloud[SoundCloud Provider]
    Server --> Deezer[Deezer Provider]
    Resolver --> SoundCloud
    Proxy[HTTP Proxy :8080] --> Resolver
```

## Stats

<div align="center">

![GitHub Stats](https://github-readme-stats.vercel.app/api?username=jumpfool&show_icons=true&theme=tokyonight)
![Top Languages](https://github-readme-stats.vercel.app/api/top-langs/?username=jumpfool&layout=compact&theme=tokyonight)

</div>

## License

Distributed under the MIT License. See `LICENSE` for more information.

---

<div align="center">

![Footer](./assets/footer.png)

**Bedrock** — The foundation of your music experience.

</div>
