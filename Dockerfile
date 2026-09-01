FROM --platform=$BUILDPLATFORM node:24-alpine AS dashboard-builder
WORKDIR /app/apps/dashboard
COPY apps/dashboard/package.json apps/dashboard/package-lock.json ./
RUN npm ci
COPY apps/dashboard ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder
ARG TARGETARCH
# Stamped into the binary because every cache key embeds version.Version: without
# this the released image reports "development" forever, the keys never rotate,
# and a release that changes a cached payload's shape silently decodes the
# previous release's entries. Release CI passes the git tag; local builds keep
# the package default, which is also what LocalCache.Clear() gates on.
ARG VERSION=development
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY ee ./ee
COPY keys ./keys
COPY config ./config
COPY updates ./updates
RUN GOOS=linux GOARCH=${TARGETARCH} go build \
    -ldflags "-X xprem/internal/version.Version=${VERSION}" \
    -o main ./cmd/api

FROM alpine:latest
# Fixed uid/gid so USER can be numeric below and volume ownership instructions
# (chown 100:101 / fsGroup: 101) stay stable across rebuilds. /app/updates is
# pre-created and chowned so the default LOCAL_BUCKET_BASE_PATH (./updates)
# stays writable without root.
RUN apk add --no-cache bash && \
    addgroup -S -g 101 ota && adduser -S -u 100 -G ota ota && \
    mkdir -p /app/updates && chown ota:ota /app/updates
WORKDIR /app
COPY --from=builder /app/main /app/main
COPY --from=dashboard-builder /app/apps/dashboard/dist /app/apps/dashboard/dist
# Numeric UID:GID (not "ota") so Kubernetes can verify runAsNonRoot: true
# without requiring an explicit runAsUser in every pod spec.
USER 100:101
EXPOSE 3000
CMD ["/app/main"]
