#!/usr/bin/env bash
# tag-release: invoked by changesets/action as the "publish" step after a
# Version Packages PR merges. Translates the package.json version into the
# Go-module tag (vX.Y.Z) and pushes it; the tag push triggers GoReleaser.
set -euo pipefail
VERSION="$(node -p "require('./package.json').version")"
TAG="v${VERSION}"
if git rev-parse -q --verify "refs/tags/${TAG}" >/dev/null; then
  echo "tag ${TAG} already exists; nothing to do"
  exit 0
fi
git tag -a "${TAG}" -m "gamekit ${TAG}"
git push origin "${TAG}"
echo "released ${TAG}"
