---
name: ella-ims
description: >
  Manages and inspects a running Ella IMS (IP Multimedia Subsystem: P-CSCF, I-CSCF,
  S-CSCF, IBCF, IP-SM-GW, MCPTT AS) through its REST API. Use when the user asks about
  live calls, call records, call outcomes, registered phones, IMPIs/IMPUs, contacts,
  SIP trunks, SIP routes, Diameter peers (HSS, PCRF, SMSC), the operator and numbering
  plan, the P-CSCF policy interface, SMS over IP routes, MCPTT users, groups and group
  calls, users and API tokens, cluster state, or metrics of an Ella IMS instance.
---

# Ella IMS

## Connection

- Base URL: `ELLA_IMS_URL`, e.g. `http://10.0.0.5:5020`. The API listens on port `5020`, over plain HTTP unless `api.tls` is set in the configuration file; use `curl -k` for a self-signed `https://` URL.
- Token: `ELLA_IMS_TOKEN`, either an API token (starts with `ellaims_`, does not expire unless set to) or an access token from login (valid 15 minutes).

Ask the user for anything not in the environment or the conversation. If the user gives an email and password instead of a token, log in:

```bash
ELLA_IMS_TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"email":"admin@example.com","password":"..."}' \
  "$ELLA_IMS_URL/api/v1/auth/login" | jq -r .result.token)
```

On a `401` with an access token, log in again.

Send `Authorization: Bearer $ELLA_IMS_TOKEN` on every request, except `GET /api/v1/status`, `/livez`, `/readyz`, `/metrics` and `/openapi.yaml`.

## Finding the endpoint

The OpenAPI spec is the source of truth. Fetch it once per session when an operation is not covered below; never guess paths or fields:

```bash
curl -s "$ELLA_IMS_URL/api/v1/openapi.yaml" | grep -E '^  /api'
```

| Question | Endpoint |
|----------|----------|
| Version, health | `GET /api/v1/status` |
| Calls, ongoing or past | `GET /api/v1/call-records` (`search`, `start`, `end`, `outcome`) |
| Calls per outcome | `GET /api/v1/call-records/counts` (same filters) |
| Registered phones | `GET /api/v1/registrations` (`search`: substring of an IMPI or IMPU) |
| SIP trunks and their state | `GET /api/v1/sip-trunks` (`status.state`: `up`, `down`, `unknown`, `unusable`) |
| Which trunks a number uses | `GET /api/v1/sip-routes` (longest `prefix` wins; `""` is the default route) |
| HSS, PCRF, SMSC connections | `GET /api/v1/diameter/peers` (`status.state`) |
| MCC, MNC, numbering plan | `GET /api/v1/operator` |
| Home domain, SIP listeners | `GET /api/v1/sip` |
| Rx, N5 or no policy | `GET /api/v1/policy` |
| SMS over IP | `GET /api/v1/ip-sm-gw`, `/api/v1/ip-sm-gw/routes` |
| Push-to-talk | `/api/v1/mcptt/*` (`404` when the MCPTT AS does not run) |
| Cluster members, leader | `GET /api/v1/cluster` |
| Counters and gauges | `GET /api/v1/metrics` (Prometheus text, `ellaims_*`) |

Responses are `{"result": ...}` or `{"error": "..."}`. Paginated lists take `page` and `per_page` (max `100`) and return `items`, `page`, `per_page`, `total_count`. SIP trunks, SIP routes and Diameter peers return `items` only.

## Live calls

There is no "ongoing" filter. Call records come newest first; an ongoing call has `in_progress: true`. A call with no `outcome` yet is still ringing. `in_trunk_name` is set for a call from a carrier, `out_trunk_name` for a call answered by one; neither for phone-to-phone.

```bash
page=1
while :; do
  resp=$(curl -s -H "Authorization: Bearer $ELLA_IMS_TOKEN" \
    "$ELLA_IMS_URL/api/v1/call-records?per_page=100&page=$page")
  live=$(echo "$resp" | jq '[.result.items[] | select(.in_progress)]')
  echo "$live" | jq -r '.[] | [.requested_at, .calling_party[-1], .called_party,
    (.outcome // "ringing"), (.in_trunk_name // .out_trunk_name // "-")] | @tsv'
  [ "$(echo "$live" | jq length)" -eq 0 ] && break
  page=$((page + 1))
done
```

This stops at the first page without an ongoing call. For a total, `ellaims_active_calls` in `/api/v1/metrics` counts calls attempted and not yet ended, and `ellaims_trunk_active_calls` counts them per trunk ID and direction.

Look up a phone by number or IMPI with `search`, e.g. `search=%2B15552100223` (URL-encode `+`). In a registration, `contacts[].address` is where the phone is, and `signalling_path: lost` means the IMS lost its signalling path.

## Changing things

Roles: Admin (`1`) does everything; Read Only (`2`) reads; Network Manager (`3`) reads, changes settings and re-authenticates registrations. A `403` means the role or the token type is not allowed; do not retry. Users, cluster, backup and restore are Admin only, and `/api/v1/users` (except `/users/me`) and creating API tokens refuse API tokens: they need an access token from login.

Before any `POST`, `PUT` or `DELETE`, say what will change and get the user's approval. `GET` the current state first, make one change at a time, and `GET` again to confirm it.

- `PUT` replaces the whole resource. For a SIP trunk, a field left out takes its default (e.g. `enabled` becomes `true`): `GET` it, edit the `result`, drop `id`, `effective`, `status` and `tls.ca_certificate_details`, and send the rest.
- A SIP route references trunks by `id`, not name. A trunk used by a route cannot be deleted (`409`).
- Changing MCC, MNC or domain with `PUT /api/v1/operator` restarts Diameter and SIP. Warn the user.
- `POST /api/v1/registrations/{impi}/reauthenticate` makes the phone's next REGISTER challenged.
- An API token is shown once, in the response to `POST /api/v1/users/me/api-tokens` (`{"name": "..."}`, optional `expires_at`).

Request bodies and constraints are in the spec, or at `https://docs.ellanetworks.com/ims/reference/api/`.
