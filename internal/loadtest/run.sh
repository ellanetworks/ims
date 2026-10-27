#!/bin/bash
# run.sh runs one load test: it builds the load generator, resets the IMS on its VM and the fakes on this host, runs
# the UEs on their VM, and prints the results with the IMS's own metrics. Every step has a hard timeout.
#
# Usage: run.sh <seconds> [loadtest flags...]
#
#   <seconds>  the expected duration of the run: it is killed 60 s after.
#
# Environment:
#   IMS_VM, LOAD_VM  the multipass VMs of the IMS and of the UEs (ims-perf, ims-load)
#   IMS_BIN          an IMS binary to deploy before the run, else the deployed one
#   MAX_CONNS        the IMS's IMS_MAX_CONNS (1048576)
#   PERF             seconds into the run to record perf on both VMs for 8 s, none if empty
#
# The IMS VM has /home/ubuntu/start.sh, which starts the IMS on a fresh database, and a route to the UEs' addresses
# through the load VM; the load VM has them as local (ip route add local 10.64.0.0/14 dev lo).
set -euo pipefail

secs=$1
shift

ims_vm=${IMS_VM:-ims-perf}
load_vm=${LOAD_VM:-ims-load}
limit=$((secs + 60))
out=$(mktemp -d)

ip_of() { multipass info "$1" --format json | python3 -c 'import json,sys; print(json.load(sys.stdin)["info"][sys.argv[1]]["ipv4"][0])' "$1"; }
on() { local vm=$1 t=$2; shift 2; timeout "$t" multipass exec "$vm" -- "$@"; }

ims_ip=$(ip_of "$ims_vm")
load_ip=$(ip_of "$load_vm")
host_ip=$(ip -4 -o route get "$ims_ip" | sed -n 's/.* src \([0-9.]*\).*/\1/p')

cd "$(dirname "$0")/../.."
go test -c -o "$out/loadtest.test" ./internal/loadtest

pkill -x loadtest.test || true
on "$load_vm" 30 sudo bash -c 'systemctl stop load 2>/dev/null; systemctl reset-failed load 2>/dev/null; rm -f /home/ubuntu/run.log; ip xfrm state flush; ip xfrm policy flush' || true

timeout 60 multipass transfer - "$load_vm:/home/ubuntu/loadtest.test" < "$out/loadtest.test"
if [ -n "${IMS_BIN:-}" ]; then
	# Renamed into place: the running IMS holds the old file.
	timeout 60 multipass transfer - "$ims_vm:/home/ubuntu/ims.new" < "$IMS_BIN"
	on "$ims_vm" 15 bash -c 'chmod +x /home/ubuntu/ims.new && mv /home/ubuntu/ims.new /home/ubuntu/ims'
fi

on "$ims_vm" 30 sudo /home/ubuntu/start.sh "${MAX_CONNS:-1048576}" >/dev/null

# The fakes, on this host: out of the UEs' way.
GOMAXPROCS=6 timeout "$limit" "$out/loadtest.test" -test.run TestLoad -test.v -test.timeout "${limit}s" -load -role fakes \
	-ims "$ims_ip" -local "$host_ip" "$@" > "$out/fakes.log" 2>&1 &
fakes=$!
trap 'kill $fakes 2>/dev/null || true' EXIT

for _ in $(seq 20); do grep -q 'fakes up' "$out/fakes.log" && break; sleep 0.5; done
grep -q 'fakes up' "$out/fakes.log" || { cat "$out/fakes.log"; exit 1; }

on "$load_vm" 30 sudo systemd-run --unit=load -p LimitNOFILE=1048576 -p RuntimeMaxSec="$limit" --setenv=GOMEMLIMIT=7GiB \
	--working-directory=/home/ubuntu bash -c "./loadtest.test -test.run TestLoad -test.v -test.timeout ${secs}s -role ues -load \
	-ims $ims_ip -local $load_ip $(printf '%q ' "$@") > run.log 2>&1" >/dev/null

if [ -n "${PERF:-}" ]; then
	(
		sleep "$PERF"
		for vm in "$ims_vm" "$load_vm"; do
			on "$vm" 60 sudo bash -c 'perf record -a -g -o /tmp/perf.data -- sleep 8 >/dev/null 2>&1; perf report -i /tmp/perf.data --no-children --sort sym --stdio 2>/dev/null | grep -E "^ +[0-9]" | head -15' > "$out/perf-$vm.txt" &
		done
		wait
	) &
	perf=$!
fi

start=$(date +%s)
while on "$load_vm" 15 systemctl is-active --quiet load; do
	if (($(date +%s) - start > limit)); then
		echo "run.sh: killed after ${limit}s" >&2
		break
	fi

	sleep 5
done

[ -n "${perf:-}" ] && wait "$perf"

on "$load_vm" 30 cat /home/ubuntu/run.log | grep -E 'load_linux_test|^(--- |PASS|FAIL|panic)' | grep -v '1× ue: install' || true

echo "--- IMS"
on "$ims_vm" 15 curl -s -m5 localhost:5020/api/v1/metrics | python3 -c '
import re, sys
m = {}
for line in sys.stdin:
    if line.startswith("#"):
        continue
    name, _, value = line.rpartition(" ")
    m[name] = float(value)
def g(k): return m.get(k, 0)
for i in ("cx", "rx"):
    n = g("ellaims_peer_request_duration_seconds_count{interface=\"%s\"}" % i)
    s = g("ellaims_peer_request_duration_seconds_sum{interface=\"%s\"}" % i)
    t = g("ellaims_peer_requests_total{interface=\"%s\",result=\"timeout\"}" % i)
    f = g("ellaims_peer_requests_total{interface=\"%s\",result=\"failure\"}" % i)
    print("%s: %d requests, mean %.1f ms, %d timeouts, %d failures" % (i, n, 1000 * s / max(n, 1), t, f))
n = g("ellaims_database_query_duration_seconds_count{pool=\"write\"}")
s = g("ellaims_database_query_duration_seconds_sum{pool=\"write\"}")
print("database writes: %d, mean %.2f ms" % (n, 1000 * s / max(n, 1)))
print("registered: %d, CPU: %.0f s, resident: %.0f MiB" % (g("ellaims_registered_subscribers"), g("process_cpu_seconds_total"), g("process_resident_memory_bytes") / 2**20))
'

for vm in "$ims_vm" "$load_vm"; do
	[ -s "$out/perf-$vm.txt" ] && { echo "--- perf $vm"; cut -c1-110 "$out/perf-$vm.txt"; }
done

echo "--- logs in $out"
