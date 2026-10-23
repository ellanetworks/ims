#!/bin/bash
# Prints the e2e Open5GS image tag: a hash of its build context.
set -euo pipefail

cd "$(dirname "$0")"
cat Dockerfile ifc.patch qos.patch signalling.patch | sha256sum | cut -c1-16
