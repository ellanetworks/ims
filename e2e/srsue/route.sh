#!/bin/bash
(
	until ip -4 addr show dev tun_srsue 2>/dev/null | grep -q inet; do sleep 0.5; done
	ip route replace "$IMS_SUBNET" dev tun_srsue
	echo "route: $IMS_SUBNET via tun_srsue ($(ip -4 -o addr show dev tun_srsue | awk '{print $4}'))"
) &
exec /entrypoint.sh "$@"
