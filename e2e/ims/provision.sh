#!/bin/bash
# Provisions the IMS of the e2e setup through its API: the Diameter peers of Open5GS, their realm, and its policy
# function. The operator settings are the database's defaults: MCC 001, MNC 01. Each step is skipped when already
# done, so that a rerun completes a partial provisioning.
set -euo pipefail

api=http://localhost:5020/api/v1

call() {
	local method=$1 path=$2 body=$3
	curl -fsS -X "$method" -H 'Content-Type: application/json' -d "$body" "$api$path" >/dev/null
}

add_peer() {
	local host=$1 body=$2
	if ! curl -fsS "$api/diameter/peers" | grep -q "\"host\": *\"$host\""; then
		call POST /diameter/peers "$body"
	fi
}

add_peer hss.localdomain '{"host": "hss.localdomain", "address": "10.80.0.10", "applications": ["cx"]}'
call PUT /diameter/routes/cx '{"realm": "localdomain"}'

case "$E2E_RAT" in
4g)
	add_peer pcrf.localdomain '{"host": "pcrf.localdomain", "address": "10.80.0.11", "applications": ["rx"]}'
	call PUT /diameter/routes/rx '{"realm": "localdomain"}'
	call PUT /policy '{"interface": "rx"}'
	;;
5g)
	call PUT /policy '{"interface": "n5", "n5": {"pcf_uri": "https://pcf.5gc.mnc001.mcc001.3gppnetwork.org:7777"}}'
	;;
esac

echo "IMS provisioned"
