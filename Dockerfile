# syntax=docker/dockerfile:1

FROM golang:1.23-alpine AS build
WORKDIR /src
# Module cache layer (no external deps, but keeps builds cache-friendly).
COPY go.mod ./
RUN go mod download
COPY . .
# Build the binary and stage an empty state dir to carry into the final image
# (distroless has no shell to mkdir/chown), made writable by the non-root user.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server && mkdir -p /data

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
# /app stays root-owned (read-only to the app) so the binary and registry
# can't be overwritten at runtime.
COPY --from=build /out/server /app/server
# The model registry (source of truth for advertised models + reasoning
# efforts). Override at runtime by mounting your own or setting MODELS_CONFIG.
COPY --from=build /src/models.json /app/models.json
# Writable state dir for STATELESS=false (owned by the non-root user). Mount a
# volume here to persist across container recreation.
COPY --from=build --chown=nonroot:nonroot /data /data
ENV TOKENS_FILE=/data/tokens.json
EXPOSE 8000
USER nonroot:nonroot
ENTRYPOINT ["/app/server"]
