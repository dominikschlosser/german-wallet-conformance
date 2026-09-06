#!/bin/sh
# Installs iOS-compatible TLS and the authorization metadata workaround.
# Usage: scripts/suite-nginx.sh [ca-dir] [nginx-container]

set -eu

CA_DIR=${1:-${DE_WALLET_CA_DIR:-$HOME/.german-wallet-conformance}}
CONTAINER=${2:-${OIDF_SUITE_NGINX_CONTAINER:-conformance-suite-nginxlocal-1}}
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
# A localhost certificate from the wallet CA, with the CA appended so nginx
# serves the chain.
HERE=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
"$HERE/localhost-cert.sh" "$WORK/suite-chain.crt" "$WORK/suite.key" "$CA_DIR"
docker cp "$WORK/suite-chain.crt" "$CONTAINER:/etc/ssl/certs/nginx-selfsigned.crt"
docker cp "$WORK/suite.key" "$CONTAINER:/etc/ssl/private/nginx-selfsigned.key"

# Install the committed configuration and metadata filter.
docker cp "$HERE/../suite/de-wallet-ios.js" "$CONTAINER:/etc/nginx/de-wallet-ios.js"
docker cp "$HERE/../suite/nginx.conf" "$CONTAINER:/etc/nginx/nginx.conf"

if ! docker exec "$CONTAINER" nginx -t >/dev/null 2>&1; then
	echo "error: the nginx configuration does not pass nginx -t" >&2
	docker exec "$CONTAINER" nginx -t
	exit 1
fi
docker exec "$CONTAINER" nginx -s reload
echo "Suite nginx configured in $CONTAINER"
