#!/bin/bash
# Prepare local services, then run issuance and presentation in the simulator.
set -euo pipefail
ROOT=$(cd -- "$(dirname -- "$0")/.." && pwd)
CA_DIR=${DE_WALLET_CA_DIR:-$HOME/.german-wallet-conformance}
RUN_DIR=${OIDF_RUN_DIR:-$CA_DIR/run/$(date +%Y%m%d-%H%M%S)}
SUITE_DIR=${OIDF_SUITE_DIR:-$ROOT/.build/suite}
BACKEND_DIR=${DE_WALLET_MOCK_BACKEND_DIR:-$ROOT/upstream/mock-backend}
BACKEND_PORT=${DE_WALLET_BACKEND_PORT:-8096}
HTTP_PORT=${DE_WALLET_BACKEND_HTTP_PORT:-8086}
BACKEND_URL=${DE_WALLET_BACKEND_URL:-https://localhost:$BACKEND_PORT}
WORKERS=1
RESUME_LOG=
PREPARE=0
UDID=
ARGS=()
while [ "$#" -gt 0 ]; do
	case "$1" in
	--workers)
		WORKERS=${2:?provide the simulator count}
		shift 2
		;;
	--resume)
		RESUME_LOG=${2:?provide the previous runner.log}
		shift 2
		;;
	--prepare)
		PREPARE=1
		shift
		;;
	*)
		ARGS+=("$1")
		shift
		;;
	esac
done
if ! [[ $WORKERS =~ ^[1-9][0-9]*$ ]] || { [ "$WORKERS" != 1 ] && { [ "$WORKERS" -lt 4 ] || [ "$((WORKERS % 2))" -ne 0 ]; }; }; then
	echo "error: --workers must be 1 or an even number of at least 4" >&2
	exit 1
fi
if [ -n "$RESUME_LOG" ]; then
	RESUME_LOG=$(cd -- "$(dirname -- "$RESUME_LOG")" && pwd)/$(basename -- "$RESUME_LOG")
	RUN_DIR=$(dirname -- "$RESUME_LOG")
fi
for tool in go git jq openssl docker java mvn xcrun; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		echo "error: $tool is required; see docs/ios-runbook.md" >&2
		exit 1
	fi
done
# Keep the Mac awake until setup and all workers finish.
caffeinate -is -w "$$" >/dev/null 2>&1 &
mkdir -p "$RUN_DIR/results" "$ROOT/.build/bin"
(cd "$ROOT" && go build -o .build/bin/conformance ./cmd/conformance)
CLI="$ROOT/.build/bin/conformance"
"$ROOT/scripts/wallet-ca.sh" "$CA_DIR"
if [ ! -f "$CA_DIR/holder-key.pem" ]; then
	openssl ecparam -name prime256v1 -genkey -noout -out "$CA_DIR/holder-key.pem"
	chmod 600 "$CA_DIR/holder-key.pem"
fi
OIDF_SUITE_DIR="$SUITE_DIR" "$ROOT/scripts/prepare-suite.sh"
if [ ! -f "$BACKEND_DIR/compose.yaml" ]; then
	git -C "$ROOT" submodule update --init --recursive
fi
(cd "$BACKEND_DIR" && UPSTREAM_REVISION=$(git -C upstream/backend rev-parse HEAD) \
MOCK_PUBLIC_URL="$BACKEND_URL" MOCK_PORT="$HTTP_PORT" \
COMPOSE_PROJECT_NAME="${DE_WALLET_COMPOSE_PROJECT:-de-wallet-ios-conformance}" \
	docker compose up -d --build --wait)
"$ROOT/scripts/localhost-cert.sh" "$RUN_DIR/localhost.crt" "$RUN_DIR/localhost.key" "$CA_DIR"
"$CLI" proxy --cert "$RUN_DIR/localhost.crt" --key "$RUN_DIR/localhost.key" \
	--port "$BACKEND_PORT" --upstream "http://localhost:$HTTP_PORT" >"$RUN_DIR/de-wallet-backend-tls.log" 2>&1 &
TLS_PID=$!
# shellcheck disable=SC2329 # Called by the EXIT trap.
cleanup() {
	kill "$TLS_PID" 2>/dev/null || true
	wait "$TLS_PID" 2>/dev/null || true
	if [ "$PREPARE" = 0 ] && [ -n "$UDID" ]; then
		xcrun simctl shutdown "$UDID" >/dev/null 2>&1 || true
	fi
}
trap cleanup EXIT
trap 'exit 130' INT TERM
for ((attempt = 0; attempt < 30; attempt++)); do
	if ! kill -0 "$TLS_PID" 2>/dev/null; then
		cat "$RUN_DIR/de-wallet-backend-tls.log" >&2
		exit 1
	fi
	if curl -kfsS --max-time 2 "$BACKEND_URL/actuator/health" >/dev/null 2>&1; then break; fi
	sleep 1
done
curl -kfsS --max-time 2 "$BACKEND_URL/actuator/health" >/dev/null
APP_LOG="$RUN_DIR/app-build.log"
if [ "$WORKERS" -gt 1 ] && [ "$PREPARE" = 0 ]; then
	WORKERS="$WORKERS" OIDF_RUN_DIR="$RUN_DIR" OIDF_SUITE_DIR="$SUITE_DIR" RESUME_LOG="$RESUME_LOG" \
		DE_WALLET_BACKEND_URL="$BACKEND_URL" "$ROOT/scripts/parallel-run.sh" ${ARGS[@]+"${ARGS[@]}"}
	exit $?
fi
"$ROOT/ios-app/simulator.sh" "$SUITE_DIR/scripts/certs-keys/vp-signing-ca.crt" | tee "$APP_LOG"
UDID=$(sed -n 's/^export DE_WALLET_IOS_UDID=//p' "$APP_LOG" | tail -1)
if [ -z "$UDID" ]; then
	echo "error: app setup did not return a simulator UDID" >&2
	exit 1
fi
if [ "$PREPARE" = 1 ]; then
	echo "Suite, backend and app prepared. Run scripts/run-conformance.sh to test."
	exit 0
fi
STATUS=0
RUN_LOG="$RUN_DIR/runner.log"
if [ -n "$RESUME_LOG" ]; then
	RUN_LOG="$RUN_DIR/continued-$(date +%s).log"
	ARGS+=(--resume "$RESUME_LOG")
fi
"$CLI" run --suite-dir "$SUITE_DIR" --backend "$BACKEND_URL" --udid "$UDID" \
	--holder-key "$CA_DIR/holder-key.pem" --results-dir "$RUN_DIR/results" \
	--runner-log "$RUN_LOG" ${ARGS[@]+"${ARGS[@]}"} || STATUS=$?
if [ -n "$RESUME_LOG" ]; then
	cat "$RESUME_LOG" "$RUN_LOG" >"$RUN_DIR/runner.log.next"
	mv "$RUN_DIR/runner.log.next" "$RUN_DIR/runner.log"
fi
echo "Runner log: $RUN_DIR/runner.log"
echo "Results: $RUN_DIR/results"
exit "$STATUS"
