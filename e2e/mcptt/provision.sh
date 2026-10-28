#!/bin/bash
# Provisions the MCPTT AS of Ella IMS of the e2e setup through the IMS's API. The users are the subscribers of provision.js, each at the
# public user identity of its MSISDN, the To of its third-party REGISTERs and the P-Asserted-Identity of its requests
# (TS 24.229 §5.4.1.7 c). The groups reference the users: the users first, then the groups with their members, each
# member's implicit_affiliation making the group one of its user's <ImplicitAffiliations> (TS 24.484 §8.3.2.7). Each
# item is a POST skipped when already done, then a PUT, so that a rerun completes a partial provisioning: a user's PUT
# clears its implicit affiliations, which its groups' PUTs then set again.
set -euo pipefail

api=http://localhost:5020/api/v1/mcptt
domain=mcptt.example.com
impu() { echo "sip:$1@ims.mnc001.mcc001.3gppnetwork.org"; }

auth="Authorization: Bearer $(cat .ims-token)"

call() {
	local method=$1 path=$2 body=$3
	curl -fsS -X "$method" -H "$auth" -H 'Content-Type: application/json' -d "$body" "$api$path" >/dev/null
}

# create posts the body to the collection unless the item of id is there.
create() {
	local collection=$1 id=$2 body=$3
	if ! curl -fs -o /dev/null -H "$auth" "$api/$collection/$id"; then
		call POST "/$collection" "$body"
	fi
}

# T2 short for TestGroupCall.
call PUT /service-configuration "{\"domain\": \"$domain\", \"transmit_time_limit_ms\": 5000}"

# user puts the user of the name and alias, at the IMPU of the number.
user() {
	local name=$1 alias=$2 number=$3
	local body="{\"mcptt_id\": \"sip:$name@$domain\", \"alias\": \"$alias\", \"impus\": [\"$(impu "$number")\"]}"
	create users "sip:$name@$domain" "$body"
	call PUT "/users/sip:$name@$domain" "$body"
}

user alice Alice 15550000001
user bob Bob 15550000002
user carol Carol 15550000003

# group puts the group of the name and display name, of TNG3 (maximum_duration_ms), the members, each of
# implicit_affiliation implicit, and the other fields of the extra JSON, if any.
group() {
	local name=$1 display=$2 tng3=$3 implicit=$4 members=$5 extra=${6:-}
	local list="" m
	for m in $members; do
		list+="${list:+, }{\"mcptt_id\": \"sip:$m@$domain\", \"implicit_affiliation\": $implicit}"
	done
	local body="{\"group_id\": \"sip:$name@$domain\", \"display_name\": \"$display\",
		\"maximum_duration_ms\": $tng3, \"members\": [$list]${extra:+, $extra}}"
	create groups "sip:$name@$domain" "$body"
	call PUT "/groups/sip:$name@$domain" "$body"
}

# Released when one participant is left (TS 24.379 §6.3.8.1 2).
group fire Fire 600000 true "alice bob carol"
# Released when its initiator leaves (TS 24.379 §6.3.8.1 3).
group police Police 600000 true "alice bob carol" '"release_on_initiator_leave": true'
# Of no implicit affiliation: carol, a member affiliated to it by no one, gets a 403 for her calls (TS 24.379
# §10.1.1.4.2 step 14 a); alice affiliates to it explicitly in TestRegisterAffiliate.
group rescue Rescue 600000 false "alice carol"
# Released when its floor is idle for T4 (TS 24.379 §6.3.8.1 1).
group quiet Quiet 600000 true "alice bob" '"hang_timer_ms": 3000'
# Released when TNG3 expires (TS 24.379 §6.3.8.1 5).
group brief Brief 4000 true "alice bob"

echo "MCPTT provisioned"
