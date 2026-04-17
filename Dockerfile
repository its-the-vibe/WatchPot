# syntax=docker/dockerfile:1

# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.26.2-alpine AS builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -trimpath -o watchpot .

# ── Runtime stage (scratch) ──────────────────────────────────────────────────
FROM scratch

# TLS root certificates required for Redis TLS connections
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Binary
COPY --from=builder /build/watchpot /watchpot

# Static assets
COPY static /static

EXPOSE 8080

ENTRYPOINT ["/watchpot"]
