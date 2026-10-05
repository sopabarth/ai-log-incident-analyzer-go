# syntax=docker/dockerfile:1

# --- build -------------------------------------------------------------------
# The Go version matches go.mod, so the toolchain never has to download another.
FROM golang:1.27-alpine AS build
WORKDIR /src

# Dependencies first, so this layer is cached until go.mod / go.sum change.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
# Static binaries (no cgo), so they run on any base image. The migrations are
# embedded in `migrate`, so it needs nothing else at runtime.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server  ./cmd/server && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate

# --- run ---------------------------------------------------------------------
# One image carries both programs: compose runs `migrate` once, then `server`.
FROM alpine:3.22
# CA certificates are needed to call the Groq API over TLS; alpine's own wget is
# what the compose health check uses.
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 app
COPY --from=build /out/server /out/migrate /app/
USER app
EXPOSE 8000
ENTRYPOINT ["/app/server"]
