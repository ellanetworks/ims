#!/bin/bash
# Issues the certificates of N5 over TLS into tls/: a CA, and for the PCF, the SCP and NRF that reach it, and the IMS
# an ECDSA P-256 certificate for both TLS client and server, valid for the domain name and the address of each
# (TS 33.310 §6.1.3c.3).
set -euo pipefail

cd "$(dirname "$0")"

dir=tls
mkdir -p "$dir"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

openssl req -x509 -new -nodes -days 30 -subj /CN=e2e-ca \
	-newkey ec -pkeyopt ec_paramgen_curve:P-256 -keyout "$tmp/ca.key" -out "$tmp/ca.crt" \
	-addext basicConstraints=critical,CA:TRUE,pathlen:0 -addext keyUsage=critical,keyCertSign,cRLSign 2>/dev/null

issue() {
	local name=$1 san=$2

	openssl req -new -nodes -subj "/CN=$name" \
		-newkey ec -pkeyopt ec_paramgen_curve:P-256 -keyout "$tmp/$name.key" -out "$tmp/$name.csr" 2>/dev/null
	openssl x509 -req -days 30 -in "$tmp/$name.csr" -CA "$tmp/ca.crt" -CAkey "$tmp/ca.key" -set_serial "0x$(openssl rand -hex 16)" \
		-extfile <(printf 'subjectAltName=%s\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=serverAuth,clientAuth\n' "$san") \
		-out "$tmp/$name.crt" 2>/dev/null
}

issue pcf DNS:pcf.5gc.mnc001.mcc001.3gppnetwork.org,IP:10.80.0.10
issue scp DNS:scp.5gc.mnc001.mcc001.3gppnetwork.org,IP:127.0.0.200
issue nrf DNS:nrf.5gc.mnc001.mcc001.3gppnetwork.org,IP:127.0.0.10
issue ims DNS:pcscf.ims.mnc001.mcc001.3gppnetwork.org,IP:10.80.0.5

# The containers read them as root.
chmod 644 "$tmp"/*.crt "$tmp"/*.key
rm -f "$tmp"/*.csr "$tmp"/ca.key
mv "$tmp"/* "$dir"/
