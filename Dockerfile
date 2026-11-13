# syntax=docker/dockerfile:1.7

# ---- build: a static binary, no cgo ----
FROM golang:1.27.4-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/api ./cmd/api

# ---- runtime: distroless, no shell, no package manager, non-root ----
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/api /api
USER nonroot:nonroot
ENV PORT=3000
EXPOSE 3000
# No curl in a distroless image: the binary checks itself.
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 CMD ["/api", "healthcheck"]
# The binary is PID 1 and handles SIGTERM/SIGINT itself (cmd/api/main.go).
ENTRYPOINT ["/api"]
