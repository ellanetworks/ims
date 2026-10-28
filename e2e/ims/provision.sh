#!/bin/bash
# Provisions the IMS of the e2e setup through its API: the Diameter peers of Open5GS, their realm, and its policy
# function, the PCRF over Rx in 4G, the PCF over N5 in 5G. The operator settings are the database's defaults: MCC 001,
# MNC 01. Each step is skipped when already done, so that a rerun completes a partial provisioning.
#
# An MCPTT call has two media components: the audio, and the application one of the floor control channel
# (m=application, TS 24.380 §4.3.3.1), of Media-Type APPLICATION as its m-line (TS 29.213 Table 6.2.1, TS 29.513
# Table 7.2.3-1), its one flow of Flow-Usage RTCP (TS 29.213 Table 6.2.2). The Open5GS PCRF and PCF authorise it, and
# the MC QoS of TS 23.379 §5.7.3, with open5gs/qos.patch only (provision.js: the PCC rules, fewer in 4G).
set -euo pipefail

api=http://localhost:5020/api/v1

auth="Authorization: Bearer $(cat .ims-token)"

call() {
	local method=$1 path=$2 body=$3
	curl -fsS -X "$method" -H "$auth" -H 'Content-Type: application/json' -d "$body" "$api$path" >/dev/null
}

add_peer() {
	local host=$1 body=$2
	if ! curl -fsS -H "$auth" "$api/diameter/peers" | grep -q "\"host\": *\"$host\""; then
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
