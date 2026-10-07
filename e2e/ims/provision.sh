#!/bin/bash
# Provisions the IMS of the e2e setup through its API: the Diameter peers of Open5GS, and its policy function. The
# operator settings are the database's defaults: MCC 001, MNC 01.
set -euo pipefail

api=http://localhost:5020/api/v1

call() {
	local method=$1 path=$2 body=$3
	curl -fsS -X "$method" -H 'Content-Type: application/json' -d "$body" "$api$path" >/dev/null
}

if [ "$(curl -fsS "$api/diameter/peers" | grep -c '"id"')" -gt 0 ]; then
	echo "IMS already provisioned"
	exit 0
fi

call POST /diameter/peers '{"host": "hss.localdomain", "realm": "localdomain", "address": "10.80.0.10", "applications": ["cx"]}'

case "$E2E_RAT" in
4g)
	call POST /diameter/peers '{"host": "pcrf.localdomain", "realm": "localdomain", "address": "10.80.0.11", "applications": ["rx"]}'
	call PUT /policy '{"interface": "rx"}'
	;;
5g)
	call PUT /policy '{"interface": "n5", "n5": {"pcf_uri": "https://pcf.5gc.mnc001.mcc001.3gppnetwork.org:7777"}}'
	;;
esac

echo "IMS provisioned"
