# syntax=docker/dockerfile:1

# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.26-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /deeznt .

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM debian:trixie-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
      ffmpeg \
      opustags \
      tzdata \
      ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /deeznt /usr/local/bin/deeznt

WORKDIR /config

ENTRYPOINT ["deeznt"]
CMD ["daemon"]
