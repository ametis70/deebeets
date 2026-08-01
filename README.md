# deebeets

A headless Go daemon that syncs your **Deezer favorites**, downloads them,
tags the files for **Navidrome**, and optionally imports them with **beets**.

State lives in SQLite and is the **source of truth**: removing a file from disk
never deletes its record, and a correctly-downloaded track is not re-fetched
just because its file is gone — unless you explicitly force it.

> Personal-use tool for your own Deezer favorites. FLAC requires a Deezer HiFi
> subscription; deebeets falls back to MP3 automatically.

## Features

- Sync favorite **albums / artists / playlists / loved tracks** (any combination).
- **Two-stage pipeline**: a batched, resumable download stage feeds an
  independent beets/post-hook import stage — slow imports never block downloads.
- **Resumable** downloads (HTTP range, cipher-aligned) that survive daemon restarts.
- **Rate-limit detection**: backs off and hard-stops before Deezer bans the account.
- **Retries** (immediate, deferred, or both).
- **Blocklist** by track/album/artist/playlist id.
- Configurable **tags** and a Navidrome-friendly **naming template**.
- Full **CLI** to sync, start/stop, inspect, blocklist, download-by-id, verify.

## Install

Requires Go 1.26+.

```sh
go build -o deebeets .
```

## Configure

Copy the example and edit it:

```sh
cp config.example.toml config.toml
```

Set your Deezer **ARL** cookie. Prefer the environment variable so the secret
stays out of the file:

```sh
export DEEBEETS_ARL="your-arl-cookie"
```

Any setting can be overridden via `DEEBEETS_`-prefixed env vars, e.g.
`DEEBEETS_DOWNLOAD_CONCURRENCY=5`. See `config.example.toml` for all options and
`deebeets config print` to view the resolved configuration.

## Usage

Run the daemon (owns the pipeline and a Unix control socket):

```sh
deebeets daemon
```

Everything else is a thin client that talks to the daemon:

```sh
# Pull favorites into the queue (does NOT download). No flags = configured defaults.
deebeets sync --tracks --albums

# Control the download queue (decoupled from sync)
deebeets start
deebeets stop

# Inspect
deebeets status
deebeets list --state queued,failed

# Download specific ids
deebeets download 3135556 --type track
deebeets download 302127  --type album

# Blocklist (never downloaded)
deebeets blocklist add 12345 --type artist --reason "not interested"
deebeets blocklist list

# Force re-download — two distinct modes
deebeets redownload --missing        # only files gone from disk (restore deletions)
deebeets redownload --all            # everything, overwriting (quality upgrade/corruption)
deebeets redownload --all 3135556    # or limit --all to specific ids

# Report finished items whose files are missing (read-only; never deletes)
deebeets verify

# Trigger a beets import on demand
deebeets beets import --path /music/Artist/Album
```

`status`, `list` and `verify` read the SQLite database directly, so they work
even when the daemon is stopped.

## How it works

1. **sync** enumerates favorites via Deezer's `gw-light` gateway and upserts one
   queue row per track (`waiting`). Albums/artists/playlists expand to tracks.
2. **download stage** (started by `start`) claims tracks in batches of
   `download.concurrency`, resolves a URL via `media.deezer.com` (with a legacy
   CDN fallback), streams and decrypts (Blowfish CBC-stripe), writes baseline
   tags, and moves the file into place under a Navidrome-friendly path.
3. **import stage** groups downloaded tracks per release and runs `beet import`
   plus any post-hooks, independently of the download stage.

### State machine

```
waiting → queued → downloading → downloaded → import_queued → importing → finished
```
plus `failed` (stage=download|import), `blocklisted`, `skipped`.

Interrupted `downloading` rows revert to `queued` on restart, keeping their
partial file so the next attempt resumes from a cipher-aligned boundary.

## Development

```sh
go test ./...
go vet ./...
```

## Acknowledgements

Deezer gateway/decryption behaviour was modelled on the excellent
[deemix](https://github.com/bambanah/deemix) reference implementation.
