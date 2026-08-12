# deeznt

A headless Go daemon that syncs your **Deezer favorites**, downloads them as FLAC (or MP3), tags them with full metadata from the Deezer API, and optionally converts them to Opus for **Navidrome**.

State lives in SQLite and is the **source of truth**: removing a file from disk never deletes its record, and a correctly-downloaded track is not re-fetched unless you explicitly force it.

> Personal-use tool for your own Deezer favorites. FLAC requires a Deezer HiFi subscription; deeznt falls back to MP3 automatically.

---

## Features

- Sync favorite **albums / artists / playlists / loved tracks** (any combination).
- **Four-stage pipeline**: sync → download → tag → convert — each independently controllable with start/stop commands.
- **Metadata cached at sync time**: full track data, lyrics, and album info are fetched from the Deezer API once during sync and stored in SQLite. Download is stream-only; tagging uses cached data — no redundant API calls.
- **Retag without redownloading**: `deeznt retag --all` re-tags all files from the DB cache. Add a new tag field or fix a bug without touching the audio data.
- **Synced lyrics** embedded in files (LRC format) and written as `.lrc` sidecar files.
- **Multi-value artist tags** (`ARTISTS`, `ALBUMARTISTS`) for correct Navidrome display.
- **Cover art** (album `cover.jpg`) and **artist images** (`folder.jpg`) fetched from CDN.
- **Rate-limit detection**: backs off and hard-stops before Deezer bans the account.
- **Batch retries**: failed downloads are retried as a group after the batch completes.
- **Blocklist** by track/album/artist/playlist id.
- Configurable **tags**, a Navidrome-friendly **naming template**, and a fully customisable **ffmpeg conversion command**.
- **Webhook notifications** for pipeline events (downloads started/finished/failed, converts finished/failed).
- Full **CLI** to manage every stage, inspect state, force redownloads/retags/reconverts.

---

## Install

Requires Go 1.26+ and `ffmpeg` + `opustags` (for opus conversion and tagging).

```sh
go build -o deeznt .
```

Or with Docker:

```sh
docker compose up --build -d
```

---

## Configure

Copy the example and edit it:

```sh
cp config.example.toml config.toml
```

Set your Deezer **ARL** cookie (find it in browser DevTools → Application → Cookies → `deezer.com`):

```sh
deeznt login
# or: export DEEZNT_ARL="your-arl-cookie"
```

Any setting can be overridden via `DEEZNT_`-prefixed env vars, e.g. `DEEZNT_DOWNLOAD_CONCURRENCY=5`. See `config.example.toml` for all options and `deeznt config print` to view the resolved configuration.

---

## Usage

Run the daemon (owns the pipeline and a Unix control socket):

```sh
deeznt daemon
```

Everything else is a thin client that talks to the daemon:

```sh
# Sync favorites — fetches and caches full metadata from Deezer
deeznt sync start --albums --tracks
deeznt sync start --refresh   # re-fetch metadata for already-known tracks

# Download queued tracks (stream-only, no API calls except token refresh)
deeznt download start
deeznt download stop           # abort after current batch

# Tag downloaded files from cached metadata (no Deezer API calls)
deeznt tag start
deeznt tag stop

# Convert tagged files via ffmpeg
deeznt convert start
deeznt convert stop

# Inspect
deeznt status
deeznt list                    # all tracks
deeznt list --albums           # album sources with state/track counts
deeznt list --state downloaded # filter by state

# Download specific ids
deeznt download start 302127 --type album
deeznt download start 3135556 --type track

# Force re-operations
deeznt redownload --failed     # retry failed downloads
deeznt redownload --missing    # re-download files missing from disk
deeznt redownload --all        # re-download everything (quality upgrade)
deeznt retag --all             # retag all files from cached DB metadata
deeznt retag --failed          # retry failed tags
deeznt reconvert --all         # reconvert all files
deeznt reconvert --failed      # retry failed conversions

# Blocklist
deeznt blocklist add 12345 --type artist --reason "not interested"
deeznt blocklist list

# Verify files on disk
deeznt verify
```

`status`, `list`, and `verify` read the SQLite database directly, so they work even when the daemon is stopped.

---

## How it works

### Pipeline stages

```
sync → download → tag → convert
```

Each stage is independently controllable. With `auto = true` in config, they chain automatically.

**1. Sync** (`deeznt sync start`)

Enumerates your Deezer favorites. For each track:
- Calls `song.getData` — stores full JSON (contributors, lyrics ID, gain, etc.) in `items.track_data`
- Calls `song.getLyrics` if available — stores JSON in `items.lyrics_data`
- Calls `album.getData` once per album — stores JSON in `album_cache`

Re-syncing refreshes metadata. `--refresh` forces re-fetch for existing items.

**2. Download** (`deeznt download start`)

For each queued track:
- Calls `song.getData` for a fresh `TRACK_TOKEN` (tokens expire ~1h), updates the cache
- Calls `media.deezer.com` to resolve the CDN URL
- Streams and decrypts (Blowfish CBC-stripe) directly to disk
- Sets state → `downloaded`

**3. Tag** (`deeznt tag start`)

For each downloaded track — **no API calls**:
- Reads `track_data`, `lyrics_data` from DB; reads album data from `album_cache`
- Builds full metadata: multi-value `ARTISTS`/`ALBUMARTISTS`, label, genre, replaygain, ISRC, copyright, LRC lyrics
- Fetches cover art and artist image from CDN using hashes stored in `track_data`
- Writes tags to the audio file
- Writes `cover.jpg` and `folder.jpg`
- Writes `.lrc` sidecar file
- Sets state → `tagged` (or → `converted` if no converter configured)

**4. Convert** (`deeznt convert start`, optional)

For each tagged track:
- Runs the configured `ffmpeg_args` command (`$source` → `$dest`)
- Writes full multi-value tags to the converted file using `opustags`
- Sets state → `converted`

### State machine

```
waiting
  ↓ sync
queued
  ↓ download stage
downloading → failed_download
  ↓
downloaded
  ↓ tag stage
tagging → failed_tag
  ↓
tagged
  ↓ convert stage (if enabled)
converting → failed_convert
  ↓
converted  ← terminal success
```

Plus `blocklisted`, `skipped`.

---

## Docker

```sh
# Start the stack (deeznt + Navidrome)
docker compose up --build -d

# First-time login (stores ARL encrypted in the DB)
docker compose exec -it deeznt deeznt login

# Trigger the first sync
docker compose exec deeznt deeznt sync start

# Open Navidrome at http://localhost:4533
```

The Docker config in `config.docker.toml` has `download.auto`, `tag.auto`, and `convert.auto` all set to `true`, so a single `sync start` chains the full pipeline automatically.

FLACs land in `/music/flac`; converted opus files land in `/music/ogg`. Navidrome scans both.

---

## Development

```sh
# Normal tests (no external tools needed)
go test ./...
go vet ./...

# Integration tests (require DEEZER_ARL, opustags, ffmpeg)
DEEZER_ARL=<arl> go test -tags integration ./internal/deezer/ -v
go test -tags integration ./internal/tagger/ -v
```

---

## Acknowledgements

Deezer gateway/decryption behaviour modelled on [deemix](https://github.com/bambanah/deemix).
