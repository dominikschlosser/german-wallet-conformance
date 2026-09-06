#!/bin/sh
# Issues a TLS certificate for localhost from the CA the German EUDI Wallet
# iOS app is built to trust, so a local server (the suite's nginx, the
# backend's TLS front, the SD-JWT issuer) can speak https to the app without
# any change to the app. Prints nothing; writes CERT and KEY.
#
# Usage: scripts/localhost-cert.sh CERT KEY [ca-dir]
#   CERT gets the certificate followed by the CA (a chain a client that
#   knows only the CA can build), KEY the private key.

set -eu

CERT=$1
KEY=$2
# The CA the app trusts (scripts/wallet-ca.sh creates it).
CA_DIR=${3:-${DE_WALLET_CA_DIR:-$HOME/.german-wallet-conformance}}
CA_CERT=$("$(dirname -- "$0")/wallet-ca.sh" "$CA_DIR")
CA_KEY="$CA_DIR/ca-key.pem"

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
cat >"$WORK/ext.cnf" <<'CNF'
[req]
distinguished_name = dn
prompt = no
[dn]
CN = localhost
[ext]
subjectAltName = DNS:localhost, IP:127.0.0.1
extendedKeyUsage = serverAuth
keyUsage = digitalSignature
basicConstraints = CA:FALSE
CNF
openssl ecparam -name prime256v1 -genkey -noout -out "$WORK/leaf.key"
openssl req -new -key "$WORK/leaf.key" -out "$WORK/leaf.csr" -config "$WORK/ext.cnf"
openssl x509 -req -in "$WORK/leaf.csr" -CA "$CA_CERT" -CAkey "$CA_KEY" -CAcreateserial -CAserial "$WORK/serial" \
	-out "$WORK/leaf.crt" -days 800 -sha256 -extfile "$WORK/ext.cnf" -extensions ext >/dev/null 2>&1
cat "$WORK/leaf.crt" "$CA_CERT" >"$CERT"
cp "$WORK/leaf.key" "$KEY"
