#!/bin/bash
# Prints the e2e Open5GS image tag, a hash of its build context.
set -euo pipefail

cd "$(dirname "$0")"
sha256sum Dockerfile | cut -c1-16
