#!/bin/bash
# Fetch, build and start the pinned suite. Keep its data and server between runs.
set -euo pipefail
ROOT=$(cd -- "$(dirname -- "$0")/.." && pwd)
SUITE=${OIDF_SUITE_DIR:-$ROOT/.build/suite}
STATE="$ROOT/.build/suite-service"
PROJECT=${OIDF_SUITE_COMPOSE_PROJECT:-german-wallet-suite}
PATCH="$ROOT/suite/sdjwt-configuration.patch"
BUILD_ONLY=0
if [ "${1:-}" = --build-only ]; then
	BUILD_ONLY=1
	shift
fi
if [ "$#" -gt 0 ]; then SUITE=$1; fi
mkdir -p "$STATE" "$(dirname -- "$SUITE")"
if [ ! -e "$SUITE/.git" ]; then
	git clone --depth 1 --branch release-v5.2.4 https://gitlab.com/openid/conformance-suite.git "$SUITE"
fi
SUITE=$(cd "$SUITE" && pwd)
if [ "$(git -C "$SUITE" rev-parse HEAD)" != ab35a8df4864da35b49eff11483e204e01aa7961 ]; then
	echo "error: the suite checkout must be release-v5.2.4" >&2
	exit 1
fi
if git -C "$SUITE" apply --check "$PATCH" 2>/dev/null; then
	git -C "$SUITE" apply "$PATCH"
elif ! git -C "$SUITE" apply --reverse --check "$PATCH" 2>/dev/null; then
	echo "error: suite changes conflict with the SD-JWT configuration patch" >&2
	exit 1
fi
FINGERPRINT=$({
	git -C "$SUITE" rev-parse HEAD
	git -C "$SUITE" diff HEAD -- src pom.xml
	cat "$PATCH"
} | shasum -a 256 | awk '{print $1}')
JAR="$SUITE/target/fapi-test-suite.jar"
STAMP="$SUITE/target/wallet-conformance-source.sha256"
if [ ! -f "$JAR" ] || [ "$(cat "$STAMP" 2>/dev/null || true)" != "$FINGERPRINT" ]; then
	RUNNING_PID=$(cat "$STATE/server.pid" 2>/dev/null || true)
	if [ -n "$RUNNING_PID" ] && kill -0 "$RUNNING_PID" 2>/dev/null; then
		echo "error: stop the managed suite before rebuilding its JAR (PID $RUNNING_PID)" >&2
		exit 1
	fi
	echo "Building OIDF release-v5.2.4 with the SD-JWT configuration patch (log: $STATE/build.log)"
	if ! (cd "$SUITE" && mvn clean package) >"$STATE/build.log" 2>&1; then
		tail -60 "$STATE/build.log" >&2
		exit 1
	fi
	printf '%s\n' "$FINGERPRINT" >"$STAMP"
fi
if [ "$BUILD_ONLY" = 1 ]; then exit 0; fi
PID_FILE="$STATE/server.pid"
PID=$(cat "$PID_FILE" 2>/dev/null || true)
if [ -n "$PID" ] && kill -0 "$PID" 2>/dev/null; then
	if ! ps -p "$PID" -o command= | grep -Fq -- "-jar $JAR"; then
		echo "error: $PID_FILE refers to another process; check it before restarting" >&2
		exit 1
	fi
elif lsof -nP -iTCP:8080 -sTCP:LISTEN >/dev/null 2>&1; then
	echo "error: port 8080 is occupied by another server; stop that server first" >&2
	exit 1
else
	PID=
fi
(cd "$SUITE" && docker compose -p "$PROJECT" -f docker-compose-dev-mac-nodocker.yml up -d --build --wait)
if [ -z "$PID" ]; then
	# Give the persistent server its own process group, beyond the runner session.
	set -m
	(cd "$SUITE" && exec nohup java -jar "$JAR" \
		--fintechlabs.devmode=true --fintechlabs.startredir=false \
		--fintechlabs.base_url=https://localhost:8443 \
		--fintechlabs.base_mtls_url=https://localhost:8444 \
		--spring.mongodb.uri=mongodb://127.0.0.1:27017/test_suite \
		>"$STATE/server.log" 2>&1 </dev/null) &
	PID=$!
	set +m
	printf '%s\n' "$PID" >"$PID_FILE"
fi
READY=0
for ((attempt = 0; attempt < 90; attempt++)); do
	if ! kill -0 "$PID" 2>/dev/null; then break; fi
	if curl -kfsS --max-time 2 https://localhost:8443/api/server 2>/dev/null | jq -e '.tag == "release-v5.2.4"' >/dev/null; then
		READY=1
		break
	fi
	sleep 1
done
if [ "$READY" != 1 ]; then
	tail -40 "$STATE/server.log" >&2
	echo "error: the suite did not become ready" >&2
	exit 1
fi
CONTAINER=$(cd "$SUITE" && docker compose -p "$PROJECT" -f docker-compose-dev-mac-nodocker.yml ps -q nginxlocal)
"$ROOT/scripts/suite-nginx.sh" "${DE_WALLET_CA_DIR:-$HOME/.german-wallet-conformance}" "$CONTAINER"
echo "OIDF suite ready at https://localhost:8443 (log: $STATE/server.log)"
