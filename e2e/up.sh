#!/bin/bash
# Starts Open5GS, Ella IMS and its MCPTT AS, and the RAN simulator with three attached UEs (env.sh selects the RAT).
set -euo pipefail

cd "$(dirname "$0")"

. ./env.sh

wait_for() {
	local what=$1 timeout=$2
	shift 2

	local deadline=$((SECONDS + timeout))
	until "$@" >/dev/null 2>&1; do
		if ((SECONDS >= deadline)); then
			echo "timed out after ${timeout}s waiting for $what" >&2
			return 1
		fi
		sleep 2
	done

	echo "ready: $what"
}

attached() {
	[ "$(docker compose logs ue1 ue2 ue3 | grep -c 'route: ')" -ge 3 ]
}

ran_listener() {
	docker compose exec -T open5gs ss -lnA sctp | grep -q ":$E2E_RAN_PORT "
}

peers_open() {
	[ "$(curl -fsS -H "Authorization: Bearer $(cat .ims-token)" localhost:5020/api/v1/diameter/peers | grep -o '"state":"open"' | wc -l)" -ge "$E2E_PEERS" ]
}

if ! docker compose pull --quiet open5gs; then
	echo "Open5GS image $OPEN5GS_TAG is not published: building it"
	docker compose build open5gs
fi

./tls.sh
docker compose build ims
docker compose up -d mongo
wait_for mongo 60 docker compose exec -T mongo mongosh --quiet --eval 'db.runCommand({ping: 1})'
{ echo "const rat = \"$E2E_RAT\";"; cat provision.js; } | docker compose exec -T mongo mongosh --quiet mongodb://localhost/open5gs

docker compose up -d open5gs ims
wait_for "IMS API" 60 curl -fsS localhost:5020/api/v1/status
./ims/token.sh
./ims/provision.sh
./mcptt/provision.sh
wait_for "Diameter peers" 120 peers_open
wait_for "S1/NG listener" 60 ran_listener

docker compose up -d
wait_for "UE attach" 240 attached
docker compose logs ue1 ue2 ue3 | grep 'route: '
