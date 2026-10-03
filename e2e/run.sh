#!/bin/bash
set -euo pipefail

cd "$(dirname "$0")"

CGO_ENABLED=0 go test -c -tags e2e -o .e2e.test .

pid() { docker inspect -f '{{.State.Pid}}' "$(docker compose ps -q "$1")"; }

exec docker run --rm --privileged --pid=host --network=none \
	-e E2E_UE1_PID="$(pid ue1)" -e E2E_UE2_PID="$(pid ue2)" \
	-v "$PWD/.e2e.test:/e2e.test:ro" \
	debian:trixie-slim /e2e.test -test.v "$@"
