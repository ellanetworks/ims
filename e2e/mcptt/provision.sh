#!/bin/bash
# Provisions the MCPTT AS of Ella IMS of the e2e setup through the IMS's API. The users are the subscribers of provision.js, each at the
# public user identity of its MSISDN, the To of its third-party REGISTERs and the P-Asserted-Identity of its requests
# (TS 24.229 §5.4.1.7 c). Users and groups reference each other: the users first, then the groups with their
# members, then the users' implicit affiliations. Each step is a PUT, or a POST skipped when already done, so that a
# rerun completes a partial provisioning.
set -euo pipefail

api=http://localhost:5020/api/v1/mcptt
domain=mcptt.example.com
impu() { echo "sip:$1@ims.mnc001.mcc001.3gppnetwork.org"; }

call() {
	local method=$1 path=$2 body=$3
	curl -fsS -X "$method" -H 'Content-Type: application/json' -d "$body" "$api$path" >/dev/null
}

# create posts the body to the collection unless the item of id is there.
create() {
	local collection=$1 id=$2 body=$3
	if ! curl -fs -o /dev/null "$api/$collection/$id"; then
		call POST "/$collection" "$body"
	fi
}

# T2 short for TestGroupCall.
call PUT /service-configuration "{\"domain\": \"$domain\", \"transmit_time_limit_ms\": 5000,
	\"fc_timers_counters\": {\"t7_floor_idle_ms\": 2000, \"c7_floor_idle\": 0}}"

create users "sip:alice@$domain" "{\"mcptt_id\": \"sip:alice@$domain\", \"impus\": [\"$(impu 15550000001)\"]}"
create users "sip:bob@$domain" "{\"mcptt_id\": \"sip:bob@$domain\", \"impus\": [\"$(impu 15550000002)\"]}"
create users "sip:carol@$domain" "{\"mcptt_id\": \"sip:carol@$domain\", \"impus\": [\"$(impu 15550000003)\"]}"

# group puts the group of the name, of TNG3 (maximum_duration_ms), the members and the other fields of the extra
# JSON, if any.
group() {
	local name=$1 tng3=$2 members=$3 extra=${4:-}
	local list="" m
	for m in $members; do
		list+="${list:+, }{\"mcptt_id\": \"sip:$m@$domain\"}"
	done
	local body="{\"group_id\": \"sip:$name@$domain\", \"invite_members\": true, \"maximum_duration_ms\": $tng3,
		\"members\": [$list]${extra:+, $extra}}"
	create groups "sip:$name@$domain" "$body"
	call PUT "/groups/sip:$name@$domain" "$body"
}

# Released when one participant is left (TS 24.379 §6.3.8.1 2).
group fire 600000 "alice bob carol"
# Released when its initiator leaves (TS 24.379 §6.3.8.1 3).
group police 600000 "alice bob carol" '"release_on_initiator_leave": true'
# carol is a member, affiliated to it by no one: her calls get a 403 (TS 24.379 §10.1.1.4.2 step 14 a).
group rescue 600000 "alice carol"
# Released when its floor is idle for T4 (TS 24.379 §6.3.8.1 1).
group quiet 600000 "alice bob" '"hang_timer_ms": 3000'
# Released when TNG3 expires (TS 24.379 §6.3.8.1 5).
group brief 4000 "alice bob"

# affiliate puts the user of the name, affiliated to the groups.
affiliate() {
	local name=$1 groups=$2 number=$3
	local list="" g
	for g in $groups; do
		list+="${list:+, }\"sip:$g@$domain\""
	done
	call PUT "/users/sip:$name@$domain" "{\"impus\": [\"$(impu "$number")\"], \"implicit_affiliations\": [$list]}"
}

affiliate alice "fire police quiet brief" 15550000001
affiliate bob "fire police quiet brief" 15550000002
affiliate carol "fire police" 15550000003

echo "MCPTT provisioned"
