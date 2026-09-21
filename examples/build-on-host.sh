#!/bin/sh
# Build a container image on your own build host and push it to a registry, so
# the production machine never needs a build toolchain.
#
# Use it when CI minutes are metered (a private repository) or the build needs an
# environment CI cannot easily provide. Run it by hand, from cron, or from a
# deckhand watch with a git trigger on the build host - production then uses an
# image trigger. See docs/en/building-elsewhere.md.
#
#   IMAGE=ghcr.io/you/shop ./build-on-host.sh
#
# Expects to run inside a checkout of the project being built.
set -eu

IMAGE="${IMAGE:?set IMAGE, e.g. ghcr.io/you/shop}"
DOCKERFILE="${DOCKERFILE:-Dockerfile}"
CONTEXT="${CONTEXT:-.}"

# A version tag alongside latest, so a rollback has something to name. Uses the
# git description when available.
if VERSION=$(git describe --tags --always --dirty 2>/dev/null); then
    :
else
    VERSION="$(date -u +%Y%m%d-%H%M%S)"
fi

# Refuse to publish an image built from uncommitted changes: nobody can later
# work out what is actually running.
case "$VERSION" in
    *-dirty)
        echo "refusing to push from a dirty working tree ($VERSION)" >&2
        echo "commit or stash your changes first" >&2
        exit 1
        ;;
esac

echo "==> testing before building"
if [ -f go.mod ]; then
    go test ./...
elif [ -f package.json ] && [ -f package-lock.json ]; then
    npm ci && npm test --if-present
fi

echo "==> building $IMAGE:$VERSION"
docker build -f "$DOCKERFILE" -t "$IMAGE:$VERSION" -t "$IMAGE:latest" "$CONTEXT"

# Push the version first: if the second push fails, latest still points at
# something that exists.
echo "==> pushing"
docker push "$IMAGE:$VERSION"
docker push "$IMAGE:latest"

DIGEST=$(docker inspect --format='{{index .RepoDigests 0}}' "$IMAGE:$VERSION" 2>/dev/null || true)
echo "==> pushed $IMAGE:$VERSION"
[ -n "$DIGEST" ] && echo "    digest: $DIGEST"
echo
echo "Production will pick this up on its next poll. To watch it happen:"
echo "    deckhand history <watch>"
