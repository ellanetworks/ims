#!/bin/bash
# Creates the admin of the IMS of the e2e setup, unless it has a user, and an API token of it, which it writes to
# .ims-token for the provisioning scripts and the tests. A token that still authenticates is kept, so that a rerun
# does not pile them up.
set -euo pipefail

api=http://localhost:5020/api/v1
file=.ims-token
credentials='{"email": "admin@example.com", "password": "e2e admin password"}'

if [ -s "$file" ] && curl -fsS -o /dev/null -H "Authorization: Bearer $(cat "$file")" "$api/users/me"; then
	exit 0
fi

post() {
	curl -fsS -X POST -H 'Content-Type: application/json' "$@"
}

post -d "$credentials" "$api/init" >/dev/null 2>&1 || true
jwt=$(post -d "$credentials" "$api/auth/login" | jq -er .result.token)
post -H "Authorization: Bearer $jwt" -d "{\"name\": \"e2e $(date +%s)\"}" "$api/users/me/api-tokens" |
	jq -er .result.token >"$file"

echo "IMS API token created"
