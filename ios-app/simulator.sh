#!/bin/bash
# Build, install and launch the upstream wallet with local simulator settings.
# Usage: ios-app/simulator.sh [reader CA certificate ...]
# See ios-app/README.md for build settings.

set -euo pipefail

REPO_ROOT=$(cd -- "$(dirname -- "$0")/.." && pwd)
ROOT_DIR="$REPO_ROOT/.build/ios"

BACKEND_URL=${DE_WALLET_BACKEND_URL:-https://localhost:8096}
SIMULATOR_NAME=${SIMULATOR_NAME:-iPhone 17}
PID_ISSUER_URL=${PID_ISSUER_URL:-https://localhost:8443/test/a/oid4vc-dev-vci-de-wallet-ios/}
DERIVED_DATA=${DERIVED_DATA:-$REPO_ROOT/.build/DerivedData}
SCHEME=${SCHEME:-IDGo Dev}
CONFIGURATION=${CONFIGURATION:-DevDebug}
BUNDLE_ID=${BUNDLE_ID:-org.sprind.wallet.dev}
CA_DER="$ROOT_DIR/Wallet/Certificates/wallet_conformance_ca.der"
mkdir -p "$DERIVED_DATA"
DERIVED_DATA=$(cd -- "$DERIVED_DATA" && pwd)
CA_PEM="$DERIVED_DATA/wallet-conformance-ca.pem"
ANCHORS=()
for anchor in "$@"; do
	ANCHORS+=("$(cd -- "$(dirname -- "$anchor")" && pwd)/$(basename -- "$anchor")")
done
DE_WALLET_BACKEND_CA=${DE_WALLET_BACKEND_CA:-${DE_WALLET_CA_DIR:-$HOME/.german-wallet-conformance}/ca-cert.pem}
DE_WALLET_BACKEND_CA="$(cd -- "$(dirname -- "$DE_WALLET_BACKEND_CA")" && pwd)/$(basename -- "$DE_WALLET_BACKEND_CA")"

if ! xcrun --find xcodebuild >/dev/null 2>&1; then
	echo "error: Xcode is not the active developer directory (sudo xcode-select -s /Applications/Xcode.app)" >&2
	exit 1
fi

if [ "${SKIP_BUILD:-0}" != 1 ]; then
	"$REPO_ROOT/ios-app/prepare.sh"
fi
cd "$ROOT_DIR"
if [ -f "$DE_WALLET_BACKEND_CA" ]; then
	echo "Using the backend CA from $DE_WALLET_BACKEND_CA"
	cp "$DE_WALLET_BACKEND_CA" "$CA_PEM"
else
	echo "error: no CA at $DE_WALLET_BACKEND_CA; run 'scripts/run-conformance.sh --prepare' in german-wallet-conformance first, or name one in DE_WALLET_BACKEND_CA" >&2
	exit 1
fi
# The app loads reader trust anchors from Wallet/Certificates.
openssl x509 -in "$CA_PEM" -outform der -out "$CA_DER"
echo "Bundled the CA as $(basename "$CA_DER")"
for anchor in "${ANCHORS[@]}"; do
	name="conformance_$(basename "${anchor%.*}" | tr -c 'A-Za-z0-9_\n' '_').der"
	if openssl x509 -in "$anchor" -inform pem -outform der -out "$ROOT_DIR/Wallet/Certificates/$name" 2>/dev/null ||
		openssl x509 -in "$anchor" -inform der -outform der -out "$ROOT_DIR/Wallet/Certificates/$name"; then
		echo "Bundled $anchor as $name"
	else
		echo "error: $anchor is neither a PEM nor a DER certificate" >&2
		exit 1
	fi
done

# A simulator by that name, created from the newest iOS runtime when absent.
UDID=${DE_WALLET_IOS_UDID:-}
if [ -z "$UDID" ] || [ "$UDID" = booted ]; then
	UDID=$(xcrun simctl list devices available -j | jq -r --arg name "$SIMULATOR_NAME" \
		'[.devices | to_entries | sort_by(.key) | reverse[] | select(.key | contains("iOS")) | .value[] | select(.name == $name) | .udid][0] // empty')
fi
if [ -z "$UDID" ]; then
	RUNTIME=$(xcrun simctl list runtimes -j | jq -r '[.runtimes[] | select(.platform == "iOS" and .isAvailable)] | sort_by(.version | split(".") | map(tonumber)) | last.identifier')
	DEVICE_TYPE=$(xcrun simctl list devicetypes -j | jq -r --arg name "${SIMULATOR_DEVICE_TYPE:-iPhone 17}" '.devicetypes[] | select(.name == $name) | .identifier')
	UDID=$(xcrun simctl create "$SIMULATOR_NAME" "$DEVICE_TYPE" "$RUNTIME")
	echo "Created simulator $SIMULATOR_NAME ($UDID)"
fi

# Successful setup hands the simulator to the runner or the caller.
# shellcheck disable=SC2329
cleanup_failed_setup() {
	if [ "$1" -ne 0 ]; then
		xcrun simctl shutdown "$UDID" >/dev/null 2>&1 || true
	fi
}
trap 'cleanup_failed_setup "$?"' EXIT

if [ "${ERASE:-0}" = "1" ]; then
	xcrun simctl shutdown "$UDID" >/dev/null 2>&1 || true
	xcrun simctl erase "$UDID"
fi
xcrun simctl boot "$UDID" >/dev/null 2>&1 || true
xcrun simctl bootstatus "$UDID" -b >/dev/null
# The app blocks its start when the device has no owner authentication
# (AppBlockingController's platformAuthentication rule). Enrolling Face ID in
# the simulator satisfies LAContext without a passcode.
xcrun simctl spawn "$UDID" notifyutil -s com.apple.BiometricKit.enrollmentChanged 1 >/dev/null 2>&1 || true
xcrun simctl spawn "$UDID" notifyutil -p com.apple.BiometricKit.enrollmentChanged >/dev/null 2>&1 || true
open -a Simulator --args -CurrentDeviceUDID "$UDID" >/dev/null 2>&1 || true

if [ "${SKIP_BUILD:-0}" != "1" ]; then
	echo "Building $SCHEME ($CONFIGURATION) for the simulator"
	if ! xcodebuild \
		-project IDGo.xcodeproj \
		-scheme "$SCHEME" \
		-configuration "$CONFIGURATION" \
		-destination "id=$UDID" \
		-derivedDataPath "$DERIVED_DATA" \
		-skipPackagePluginValidation \
		-skipMacroValidation \
		VCI_ISSUER_URL="$PID_ISSUER_URL" \
		WALLET_HOST_URL="$BACKEND_URL" \
		FEATURE_FLAG_BASE_URL="$BACKEND_URL" \
		CODE_SIGN_IDENTITY="-" \
		CODE_SIGNING_REQUIRED=NO \
		DEVELOPMENT_TEAM="" \
		build >"$DERIVED_DATA/xcodebuild.log" 2>&1; then
		tail -60 "$DERIVED_DATA/xcodebuild.log" >&2
		echo "error: build failed, see $DERIVED_DATA/xcodebuild.log" >&2
		exit 1
	fi
	echo "Build succeeded. Log: $DERIVED_DATA/xcodebuild.log"
fi

APP="$DERIVED_DATA/Build/Products/$CONFIGURATION-iphonesimulator/IDGo.app"
if [ ! -d "$APP" ]; then
	echo "error: no app at $APP" >&2
	exit 1
fi

echo "Trusting the wallet CA in the simulator"
xcrun simctl keychain "$UDID" add-root-cert "$CA_PEM"
echo "Installing $(basename "$APP")"
xcrun simctl install "$UDID" "$APP"
xcrun simctl terminate "$UDID" "$BUNDLE_ID" >/dev/null 2>&1 || true
xcrun simctl launch "$UDID" "$BUNDLE_ID" >/dev/null
echo "Launched $BUNDLE_ID in $SIMULATOR_NAME ($UDID)"
echo "export DE_WALLET_IOS_UDID=$UDID"
