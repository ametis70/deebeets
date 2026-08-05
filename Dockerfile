# syntax=docker/dockerfile:1

# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.24-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /deebeets .

# ── Runtime stage ─────────────────────────────────────────────────────────────
# beets requires Python; use a slim Debian image that has pip available.
FROM python:3.12-slim-bookworm

# System deps: ffmpeg (replaygain backend), git (beets may need it), ca-certs
RUN apt-get update && apt-get install -y --no-install-recommends \
      ffmpeg \
      ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Install beets + plugins used in beets.yaml
RUN pip install --no-cache-dir \
      beets \
      beets-deezer \
      beets-multiartist

# Copy the compiled binary
COPY --from=builder /deebeets /usr/local/bin/deebeets

# Working directory; config.toml, beets.yaml, DB and socket live here
WORKDIR /config

ENTRYPOINT ["deebeets"]
CMD ["daemon"]
