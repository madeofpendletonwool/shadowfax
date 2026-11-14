# Both build stages run on the runner's native platform and cross-compile.
# Running them under QEMU for arm64 was slow and crashed node outright
# ("qemu: uncaught target signal 4 (Illegal instruction)"), hanging the build.

# Stage 1: Build frontend. Its output is platform-independent, so build it once.
FROM --platform=$BUILDPLATFORM node:20-alpine AS frontend-builder
WORKDIR /app/frontend
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

# Stage 2: Build Go backend, cross-compiled for the target platform.
# modernc.org/sqlite is pure Go, so CGO is not needed.
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS backend-builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /app/backend
COPY backend/go.mod backend/go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY backend/ ./
COPY --from=frontend-builder /app/backend/static ./static
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o quickfiles .

# Stage 3: Final image (the only stage that runs per platform)
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=backend-builder /app/backend/quickfiles .
RUN mkdir -p /data/files
VOLUME ["/data"]
ENV DATA_DIR=/data
EXPOSE 8080
CMD ["./quickfiles"]
