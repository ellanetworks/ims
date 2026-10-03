# Sourced by up.sh and run.sh. E2E_RAT selects the access: 4g (Open5GS EPC,
# srsRAN) or 5g (Open5GS 5GC, UERANSIM).

OPEN5GS_TAG=$(./open5gs/tag.sh)
export OPEN5GS_TAG

case "${E2E_RAT:-}" in
4g)
	export COMPOSE_FILE=compose.yaml:compose.4g.yaml
	E2E_PEERS=2
	E2E_TUN=tun_srsue
	E2E_RAN_PORT=36412
	;;
5g)
	export COMPOSE_FILE=compose.yaml:compose.5g.yaml
	E2E_PEERS=1
	E2E_TUN=uesimtun0
	E2E_RAN_PORT=38412
	;;
*)
	echo "E2E_RAT must be 4g or 5g, not '${E2E_RAT:-}'" >&2
	exit 1
	;;
esac
