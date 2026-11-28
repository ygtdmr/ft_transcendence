#!/bin/sh
# Creates a self-signed TLS certificate for the local ingress if none exists.
# It is for local runs only; a real deployment supplies a certificate from a CA
# at certs/server.crt and certs/server.key (docs/DEPLOY.md).
set -eu
umask 077

dir=${CERTS_DIR:-certs}
if [ -s "$dir/server.crt" ] && [ -s "$dir/server.key" ]; then
	exit 0
fi

mkdir -p "$dir"
chmod 0700 "$dir"
openssl req -x509 -newkey rsa:3072 -sha256 -days 90 -nodes \
	-keyout "$dir/server.key" -out "$dir/server.crt" \
	-subj "/CN=localhost" \
	-addext "subjectAltName=DNS:localhost,IP:127.0.0.1" 2>/dev/null
# Compose bind-mounts both files into the ingress container, which cannot read
# a key that only its host owner may read; the 0700 directory keeps other users
# of the host out.
chmod 0444 "$dir/server.crt" "$dir/server.key"
echo "created $dir/server.crt (self-signed, 90 days, local use only)"
