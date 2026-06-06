#!/usr/bin/env bash
# tag-release: invoked by changesets/action as the "publish" step after a
# Version Packages PR merges. Translates the package.json version into the
# Go-module tag (vX.Y.Z) and pushes it; the tag push triggers GoReleaser.
set -euo pipefail
VERSION="$(node -p "require('./package.json').version")"
TAG="v${VERSION}"
# Go semantic-import-versioning guard: a module path suffixed /vN MUST be
# tagged vN.x.y — `go get` refuses a mismatched tag outright. (The v2 module
# was once nearly released as v1.0.0; changesets knows npm semver, not Go
# module majors, so this script is the place that has to know.)
MODULE_MAJOR="$(sed -n 's|^module .*/v\([0-9][0-9]*\)$|\1|p' go.mod)"
MODULE_MAJOR="${MODULE_MAJOR:-1}"
TAG_MAJOR="${VERSION%%.*}"
if [ "${TAG_MAJOR}" != "${MODULE_MAJOR}" ] && ! { [ "${MODULE_MAJOR}" = "1" ] && [ "${TAG_MAJOR}" = "0" ]; }; then
  echo "FATAL: package.json version ${VERSION} (major ${TAG_MAJOR}) does not match go.mod module major /v${MODULE_MAJOR}; refusing to tag" >&2
  exit 1
fi
if git rev-parse -q --verify "refs/tags/${TAG}" >/dev/null; then
  echo "tag ${TAG} already exists; nothing to do"
  exit 0
fi
git tag -a "${TAG}" -m "gamekit ${TAG}"
git push origin "${TAG}"
echo "released ${TAG}"
