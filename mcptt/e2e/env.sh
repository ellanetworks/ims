# Sourced by up.sh and run.sh. E2E_RAT selects the access: 4g (Open5GS EPC, srsRAN) or 5g (Open5GS 5GC, UERANSIM).
# ELLA_IMS_IMAGE is the Ella IMS under test, which up.sh builds from ELLA_IMS_SRC.

OPEN5GS_TAG=$(./open5gs/tag.sh)
export OPEN5GS_TAG

# Ella IMS is built from this tree (ims/e2e/ims/Dockerfile), or ELLA_IMS_SRC, the path of another checkout of ims.
ELLA_IMS_SRC=${ELLA_IMS_SRC:-$(cd ../.. && pwd)}
export ELLA_IMS_SRC
ELLA_IMS_IMAGE=${ELLA_IMS_IMAGE:-ella-mcptt-e2e/ims:local}
export ELLA_IMS_IMAGE

case "${E2E_RAT:-}" in
4g)
	export COMPOSE_FILE=compose.yaml:compose.4g.yaml
	# The HSS (Cx) and the PCRF (Rx).
	E2E_PEERS=2
	E2E_TUN=tun_srsue
	E2E_RAN_PORT=36412
	;;
5g)
	export COMPOSE_FILE=compose.yaml:compose.5g.yaml
	# The HSS (Cx); the PCF is reached over N5.
	E2E_PEERS=1
	E2E_TUN=uesimtun0
	E2E_RAN_PORT=38412
	;;
*)
	echo "E2E_RAT must be 4g or 5g, not '${E2E_RAT:-}'" >&2
	exit 1
	;;
esac
