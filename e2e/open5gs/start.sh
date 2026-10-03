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
nfs=(nrfd scpd hssd pcrfd upfd sgwud smfd sgwcd mmed)
for nf in "${nfs[@]}"; do
	: > "/var/log/open5gs/${nf%d}.log"
	"open5gs-$nf" >/dev/null 2>&1 &
	sleep 0.3
done

exec tail -n +1 -F /var/log/open5gs/*.log
