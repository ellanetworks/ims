#!/bin/bash
set -euo pipefail

ip addr add 10.80.0.11/24 dev eth0 2>/dev/null || true

for i in 1 2; do
	dev=ogstun$([ $i = 1 ] || echo $i)
	ip tuntap add name "$dev" mode tun 2>/dev/null || true
	ip link set "$dev" up
done
ip addr replace 10.45.0.1/16 dev ogstun
ip addr replace 10.46.0.1/16 dev ogstun2

sysctl -qw net.ipv4.ip_forward=1

mkdir -p /var/log/open5gs
case "${CORE:-}" in
4g) nfs=(nrfd scpd hssd pcrfd "upfd -c /etc/open5gs/upf-4g.yaml" sgwud smfd sgwcd mmed) ;;
5g) nfs=(nrfd scpd hssd udrd udmd ausfd pcfd bsfd nssfd "upfd -c /etc/open5gs/upf-5g.yaml" smfd amfd) ;;
*) echo "CORE must be 4g or 5g, not '${CORE:-}'" >&2; exit 1 ;;
esac

for nf in "${nfs[@]}"; do
	read -r name args <<<"$nf"
	: > "/var/log/open5gs/${name%d}.log"
	# shellcheck disable=SC2086
	"open5gs-$name" $args >/dev/null 2>&1 &
	sleep 0.3
done

exec tail -n +1 -F /var/log/open5gs/*.log
