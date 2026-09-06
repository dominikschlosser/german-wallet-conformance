#!/bin/bash
# Assemble the simulator source in .build/ios without changing the submodule.
set -euo pipefail

ROOT=$(cd -- "$(dirname -- "$0")/.." && pwd)
SOURCE="$ROOT/upstream/ios"
BUILD="$ROOT/.build/ios"

if [ ! -f "$SOURCE/.git" ]; then
	git -C "$ROOT" submodule update --init --recursive -- upstream/ios
fi
if [ -n "$(git -C "$SOURCE" status --porcelain)" ]; then
	echo "error: upstream/ios has local changes; build from a clean submodule" >&2
	exit 1
fi

mkdir -p "$BUILD"
# Refresh generated files too, so removed resources cannot survive a rebuild.
rsync -a --delete --exclude=.git "$SOURCE/" "$BUILD/"
cp -R "$ROOT/ios-app/overlay/." "$BUILD/"
echo "Prepared upstream iOS $(git -C "$SOURCE" rev-parse --short HEAD) in $BUILD"
