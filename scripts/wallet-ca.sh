#!/bin/sh
# The CA the German EUDI Wallet iOS app is built to trust for everything
# local: the suite's nginx, the backend's TLS front and the SD-JWT issuer
# all carry certificates from it. Created once, kept in DE_WALLET_CA_DIR
# (default ~/.german-wallet-conformance). Prints the certificate's path.
#
# Usage: scripts/wallet-ca.sh [ca-dir]

set -eu

CA_DIR=${1:-${DE_WALLET_CA_DIR:-$HOME/.german-wallet-conformance}}
CA_CERT="$CA_DIR/ca-cert.pem"
CA_KEY="$CA_DIR/ca-key.pem"
if [ -e "$CA_CERT" ] || [ -e "$CA_KEY" ]; then
	if [ ! -f "$CA_CERT" ] || [ ! -f "$CA_KEY" ]; then
		echo "error: incomplete CA in $CA_DIR; restore the missing certificate or key" >&2
		exit 1
	fi
else
	mkdir -p "$CA_DIR"
	WORK=$(mktemp -d)
	trap 'rm -rf "$WORK"' EXIT
	cat >"$WORK/ca.cnf" <<'CNF'
[req]
distinguished_name = dn
prompt = no
x509_extensions = ext
[dn]
C = DE
CN = German Wallet Conformance CA
[ext]
basicConstraints = critical, CA:TRUE
keyUsage = critical, keyCertSign, cRLSign
subjectKeyIdentifier = hash
CNF
	openssl ecparam -name prime256v1 -genkey -noout -out "$CA_KEY"
	openssl req -new -x509 -key "$CA_KEY" -out "$CA_CERT" -days 3650 -sha256 -config "$WORK/ca.cnf"
	chmod 600 "$CA_KEY"
	echo "Created the wallet CA in $CA_DIR" >&2
fi
printf '%s\n' "$CA_CERT"
