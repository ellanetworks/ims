#!/bin/bash
(
	until ip -4 addr show dev "$TUN" 2>/dev/null | grep -q inet; do sleep 0.5; done
	ip route replace "$IMS_SUBNET" dev "$TUN"
	echo "route: $IMS_SUBNET via $TUN ($(ip -4 -o addr show dev "$TUN" | awk '{print $4}'))"
) &
exec "$@"
