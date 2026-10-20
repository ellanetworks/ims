#!/bin/bash
# Prints the e2e Open5GS image tag: a hash of its build context.
set -euo pipefail

cd "$(dirname "$0")"
cat Dockerfile ./*.patch | sha256sum | cut -c1-16
