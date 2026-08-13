<div align="center">

# deeznt

![license](https://img.shields.io/github/license/ametis70/deeznt?style=flat-square)
![go](https://img.shields.io/badge/go-1.26-00ADD8?style=flat-square&logo=go&logoColor=white)
![nix](https://img.shields.io/badge/nix-flake-5277C3?style=flat-square&logo=nixos&logoColor=white)

deeznt syncs your **Deezer favorites**, downloads them as FLAC, tags them with full metadata, and optionally converts to Opus — ready for **Navidrome**

</div>

## What it does

- Syncs favorite **albums**, artists, playlists, and loved tracks from the Deezer API
- Downloads audio in FLAC (or MP3) with full metadata: multi-value artist tags, lyrics, label, genre, ReplayGain, cover art, and artist images
- Caches all track/album/lyrics metadata in SQLite at sync time — tagging requires no further API calls
- Converts to **Opus** via ffmpeg with correct multi-value Vorbis comments written by `opustags`
- Embeds synced lyrics in files and writes `.lrc` sidecar files
- Detects **replaced** albums and tracks replacements without re-downloading already-present files
- Rate-limit detection: backs off and hard-stops before Deezer bans the account
- Sends **webhook notifications** for pipeline events with configurable auth headers
- Full CLI to manage every stage: sync, download, tag, convert, retag, reconvert, blocklist, verify

## Pipeline

```
sync → download → tag → convert
```

Each stage is independently controllable with `start`/`stop` commands. With `auto = true` in config they chain automatically.

| Stage        | What it does                                                                                       |
| ------------ | -------------------------------------------------------------------------------------------------- |
| **sync**     | Fetches favorites from Deezer; caches `song.getData`, `song.getLyrics`, `album.getData` in SQLite  |
| **download** | Streams and decrypts audio (Blowfish CBC-stripe) to disk; token refresh only, no metadata fetching |
| **tag**      | Reads cached JSON from DB; writes FLAC/MP3 tags, cover art, artist images, `.lrc` files            |
| **convert**  | Runs ffmpeg + opustags to produce correctly-tagged Opus files                                      |

## Install

Requires Go 1.26+, `ffmpeg`, and `opustags`.

```sh
go build -o deeznt .
```

Or with Docker:

```sh
docker compose up --build -d
docker compose exec -it deeznt deeznt login
docker compose exec deeznt deeznt sync start
```

## Configuration

Copy the example and edit:

```sh
cp config.example.toml config.toml
```

Store your Deezer ARL cookie (find it in browser DevTools → Application → Cookies → `deezer.com`):

```sh
deeznt login
# or: export DEEZNT_ARL="your-arl-cookie"
```

Any setting can be overridden via `DEEZNT_`-prefixed env vars. Key options:

```toml
[sync]
interval = "6h"            # auto-sync interval; "0s" disables

[download]
auto = true                # chain download after sync

[tag]
auto = true                # chain tag after download

[convert]
enabled = true
auto    = true
dest    = "/music/ogg"
format  = "opus"
ffmpeg_args = "ffmpeg -i $source -y -vn -c:a libopus -b:a 160k -vbr on -compression_level 10 $dest"

[notifications]
webhook_url = "https://your-endpoint/hook"
auth_header = "Authorization"
auth_value  = ""           # prefer DEEZNT_WEBHOOK_AUTH_VALUE env var
```

See `config.example.toml` for all options and `deeznt config print` to view the resolved configuration.

## Usage

```sh
# Run the daemon
deeznt daemon

# Sync favorites, then download → tag → convert automatically
deeznt sync start

# Manual per-stage control
deeznt download start
deeznt download stop
deeznt tag start
deeznt convert start

# Inspect
deeznt status
deeznt list                            # PRESENT tracks only
deeznt list --deezer-status replaced   # replaced tracks
deeznt list --albums                   # album sources
deeznt list --albums --deezer-status all

# Force re-operations
deeznt redownload --failed
deeznt redownload --all
deeznt retag --all
deeznt reconvert --all

# Blocklist
deeznt blocklist add 12345 --type artist --reason "not interested"

# Verify files on disk
deeznt verify

# Test webhook
deeznt notify test
```

`status`, `list`, and `verify` read the SQLite database directly and work without the daemon running.

## State machine

```
waiting → queued → downloading → downloaded → tagging → tagged → converting → converted
                                                                              (terminal)
```

Failure states: `failed_download`, `failed_tag`, `failed_convert`.

REPLACED tracks (Deezer STATUS=3) are stored with `deezer_status=REPLACED` and never downloaded — only their replacements are queued. Re-syncing mirrors the replacement's state back to the original.

## Development

```sh
# Enter the dev shell
nix develop

# Run tests
go test ./...

# Integration tests (require DEEZER_ARL, opustags, ffmpeg)
DEEZER_ARL=<arl> go test -tags integration ./internal/deezer/ -v
go test -tags integration ./internal/tagger/ -v
```

The `internal/pipeline_test/` package contains full sync→download→tag integration tests using a mock Deezer HTTP server — no real API calls needed.

## Acknowledgements

Deezer gateway/decryption behaviour modelled on [deemix](https://github.com/bambanah/deemix). See [docs/deezer-api.md](docs/deezer-api.md) for the reverse-engineered API reference.
