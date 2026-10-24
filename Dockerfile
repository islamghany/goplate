# syntax=docker/dockerfile:1

# ---- build ------------------------------------------------------------------
FROM golang:1.27-alpine AS build
WORKDIR /src

# Dependencies first: this layer is cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
ARG VERSION=dev
ARG COMMIT=none
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" -o /out/api ./cmd/api && \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" -o /out/admin ./cmd/admin

# ---- runtime ----------------------------------------------------------------
# distroless/static: no shell, no package manager, runs as non-root.
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/api /out/admin /app/
USER nonroot:nonroot
EXPOSE 8080 9090
ENTRYPOINT ["/app/api"]
