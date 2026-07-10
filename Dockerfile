# syntax=docker/dockerfile:1

FROM golang:1.23-alpine AS build
WORKDIR /src
# Module cache layer (no external deps, but keeps builds cache-friendly).
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /server
EXPOSE 8000
USER nonroot:nonroot
ENTRYPOINT ["/server"]
