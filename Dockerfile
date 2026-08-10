# syntax=docker/dockerfile:1

# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.26-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /deebeets .

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
      ffmpeg \
      opustags \
      ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /deebeets /usr/local/bin/deebeets

WORKDIR /config

ENTRYPOINT ["deebeets"]
CMD ["daemon"]
