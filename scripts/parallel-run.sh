#!/bin/bash
# Each issuance worker has its own simulator and issuer alias.
set -euo pipefail
ROOT=$(cd -- "$(dirname -- "$0")/.." && pwd)
: "${OIDF_RUN_DIR:?set OIDF_RUN_DIR}"
: "${OIDF_SUITE_DIR:?set OIDF_SUITE_DIR}"
CLI="$ROOT/.build/bin/conformance"
RUN_DIR=$OIDF_RUN_DIR
MANIFEST="$RUN_DIR/workers.tsv"
RESUME=${RESUME_LOG:-}
WORKERS=${WORKERS:-4}
VCI_WORKERS=$(((WORKERS - 2) / 2))
LOGS=()
SIMULATORS=()
COMMON=(--suite-dir "$OIDF_SUITE_DIR" --backend "${DE_WALLET_BACKEND_URL:-https://localhost:8096}" --holder-key "${DE_WALLET_CA_DIR:-$HOME/.german-wallet-conformance}/holder-key.pem" --results-dir "$RUN_DIR/results")

# shellcheck disable=SC2329
cleanup() {
	if [ "${#SIMULATORS[@]}" -gt 0 ]; then
		printf '%s\n' "${SIMULATORS[@]}" | awk 'NF && !seen[$0]++' | while IFS= read -r udid; do
			xcrun simctl shutdown "$udid" >/dev/null 2>&1 || true
		done
	fi
	# Append whole logs, keeping every configuration/plan pair together.
	if [ "${#LOGS[@]}" -gt 0 ]; then
		for log in "${LOGS[@]}"; do [ ! -f "$log" ] || cat "$log"; done >"$RUN_DIR/runner.log.next"
		mv "$RUN_DIR/runner.log.next" "$RUN_DIR/runner.log"
	fi
}
trap cleanup EXIT
trap 'exit 130' INT TERM

if [ -n "$RESUME" ]; then
	if [ ! -s "$MANIFEST" ]; then
		echo "error: parallel resume needs $MANIFEST" >&2
		exit 1
	fi
else
	if [ "${SKIP_BUILD:-0}" = 1 ]; then
		echo "error: parallel setup builds separate issuer aliases; omit SKIP_BUILD" >&2
		exit 1
	fi
	if [ -e "$RUN_DIR/runner.log" ] || [ -e "$MANIFEST" ]; then
		echo "error: use a fresh run directory or --resume" >&2
		exit 1
	fi
	# The seed is a selected conformance module. Resume skips it later.
	for format in sdjwt mdoc; do
		for ((shard = 0; shard < VCI_WORKERS; shard++)); do
			alias="wallet-vci-$format"
			[ "$VCI_WORKERS" = 1 ] || alias="$alias-$shard"
			app_log="$RUN_DIR/app-vci-$format-$shard.log"
			SIMULATOR_NAME="Wallet VCI $format $shard $(basename "$RUN_DIR")" DE_WALLET_IOS_UDID='' PID_ISSUER_URL="https://localhost:8443/test/a/$alias/" \
				"$ROOT/ios-app/simulator.sh" "$OIDF_SUITE_DIR/scripts/certs-keys/vp-signing-ca.crt" | tee "$app_log"
			udid=$(sed -n 's/^export DE_WALLET_IOS_UDID=//p' "$app_log" | tail -1)
			[ -n "$udid" ] || {
				echo "error: missing simulator UDID" >&2
				exit 1
			}
			SIMULATORS+=("$udid")
			printf 'vci-%s\t%s\t%s\t%s\t%s\n' "$format" "$udid" "$alias" "$shard" "$VCI_WORKERS" >>"$MANIFEST"
			seed="$RUN_DIR/seed-$format-$shard.log"
			LOGS+=("$seed")
			"$CLI" run "${COMMON[@]}" --udid "$udid" --vci-alias "$alias" \
				--shard-index "$shard" --shard-count "$VCI_WORKERS" \
				--only "vci-final-$format-preauth-byval-immediate-plain" --exclude '' --rerun 1:1 --runner-log "$seed"
			app=$(xcrun simctl get_app_container "$udid" "${BUNDLE_ID:-org.sprind.wallet.dev}" app)
			xcrun simctl shutdown "$udid"
			if [ -s "$seed" ]; then
				vp_udid=$(xcrun simctl clone "$udid" "Wallet VP $format $(basename "$RUN_DIR")")
				SIMULATORS+=("$vp_udid")
				# Reinstall so the clone uses its own app container.
				xcrun simctl boot "$vp_udid"
				xcrun simctl bootstatus "$vp_udid" -b >/dev/null
				xcrun simctl install "$vp_udid" "$app"
				xcrun simctl launch "$vp_udid" "${BUNDLE_ID:-org.sprind.wallet.dev}" >/dev/null
				xcrun simctl terminate "$vp_udid" "${BUNDLE_ID:-org.sprind.wallet.dev}"
				xcrun simctl shutdown "$vp_udid"
				printf 'vp-%s\t%s\t%s\t0\t1\n' "$format" "$vp_udid" "$alias" >>"$MANIFEST"
			fi
		done
	done
	RESUME="$RUN_DIR/seeds.log"
	cat "${LOGS[@]}" >"$RESUME"
fi

exec "$CLI" parallel --workers "$WORKERS" --run-dir "$RUN_DIR" --resume "$RESUME" \
	--suite-dir "$OIDF_SUITE_DIR" --backend "${DE_WALLET_BACKEND_URL:-https://localhost:8096}" \
	--holder-key "${DE_WALLET_CA_DIR:-$HOME/.german-wallet-conformance}/holder-key.pem" -- "$@"
