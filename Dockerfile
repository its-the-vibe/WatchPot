# syntax=docker/dockerfile:1

# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.27.1-alpine AS builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -trimpath -o watchpot .

# ── Runtime stage (scratch) ──────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian13:nonroot

WORKDIR /

# Binary
COPY --from=builder /build/watchpot /watchpot

# Static assets
COPY static /static

EXPOSE 8080

USER nonroot:nonroot

ENTRYPOINT ["/watchpot"]
