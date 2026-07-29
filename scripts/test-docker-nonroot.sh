#!/usr/bin/env sh
# Verifies the runtime image's non-root contract:
#   1. The container runs as numeric uid 100 / gid 101 (so Kubernetes can
#      verify `runAsNonRoot: true` without an explicit runAsUser).
#   2. Default local storage mode works without root: /app/updates is
#      writable, so migrations (.migrationhistory) and boot succeed with no
#      volume mounted.
#   3. (Linux only) A root-owned bind mount is correctly rejected — this is
#      the scenario the docs tell users to fix with chown 100:101 / fsGroup.
#
# Usage: ./scripts/test-docker-nonroot.sh [image-tag]
set -eu

IMAGE="${1:-xprem:nonroot-test}"
CONTAINER="xprem-nonroot-test"

fail() { echo "FAIL: $1" >&2; docker logs "$CONTAINER" 2>&1 | tail -5 >&2 || true; docker rm -f "$CONTAINER" >/dev/null 2>&1 || true; exit 1; }
cleanup() { docker rm -f "$CONTAINER" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "==> Building image"
docker build -q -t "$IMAGE" .

echo "==> 1/3 container runs as numeric uid 100 / gid 101"
uid="$(docker run --rm "$IMAGE" id -u)"
gid="$(docker run --rm "$IMAGE" id -g)"
[ "$uid" = "100" ] || fail "expected uid 100, got $uid"
[ "$gid" = "101" ] || fail "expected gid 101, got $gid"
echo "    ok (uid=$uid gid=$gid)"

echo "==> 2/3 default local mode boots without root (writes to /app/updates)"
docker run -d --name "$CONTAINER" -p 13000:3000 \
  -e STORAGE_MODE=local \
  -e BASE_URL=http://localhost:13000 \
  -e JWT_SECRET=test-secret \
  -e EXPO_ACCESS_TOKEN=test -e EXPO_APP_ID=test \
  "$IMAGE" >/dev/null
i=0
until curl -sf http://localhost:13000/hc >/dev/null 2>&1; do
  i=$((i+1)); [ "$i" -lt 20 ] || fail "server did not become healthy on default LOCAL_BUCKET_BASE_PATH"
  sleep 0.5
done
# migrations write .migrationhistory into the bucket path — prove the write happened as uid 100
owner="$(docker exec "$CONTAINER" stat -c '%u' /app/updates/.migrationhistory 2>/dev/null || echo missing)"
[ "$owner" = "100" ] || fail "expected .migrationhistory owned by uid 100, got: $owner"
docker rm -f "$CONTAINER" >/dev/null
echo "    ok"

echo "==> 3/3 root-owned bind mount is rejected (Linux only)"
if [ "$(uname -s)" = "Linux" ]; then
  tmpdir="$(mktemp -d)"
  # root-owned, no group/other write — the situation the docs warn about
  chmod 755 "$tmpdir"
  docker run -d --name "$CONTAINER" \
    -e STORAGE_MODE=local -e LOCAL_BUCKET_BASE_PATH=/updates \
    -e BASE_URL=http://localhost:3000 -e JWT_SECRET=test-secret \
    -e EXPO_ACCESS_TOKEN=test -e EXPO_APP_ID=test \
    -v "$tmpdir":/updates "$IMAGE" >/dev/null
  sleep 3
  if docker exec "$CONTAINER" true 2>/dev/null && [ "$(docker inspect -f '{{.State.Running}}' "$CONTAINER")" = "true" ]; then
    fail "expected boot failure on root-owned bind mount, but server is running"
  fi
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  rm -rf "$tmpdir"
  echo "    ok (boot failed as expected; fix per docs: chown 100:101)"
else
  echo "    skipped (bind-mount ownership semantics only apply on Linux hosts)"
fi

echo "PASS: non-root contract holds"
