---
description: Reference for observability features in Ella IMS.
---

# Observability

Ella IMS supports four observability pillars: Metrics, Traces, Logs and Profiles.

## Metrics

Ella IMS exposes Prometheus metrics at `GET /api/v1/metrics`.

| Metric | Type | Description |
| --- | --- | --- |
| `ellaims_build_info` | Gauge | Always 1. The version and revision of the running build are in its labels. |
| `ellaims_registered_subscribers` | Gauge | Subscribers registered now on this node. |
| `ellaims_registration_attempts_total` | Counter | Registrations and re-registrations, by result: `accept`, `auth_failure`, `reject`. |
| `ellaims_active_calls` | Gauge | Calls in progress. |
| `ellaims_calls_total` | Counter | Calls that ended, by outcome: `answered`, `cancelled`, `busy`, `rejected`, `no_answer`, `unavailable`, `failed`. |
| `ellaims_sip_responses_total` | Counter | Final responses the P-CSCF sent to phones, by method and status class. |
| `ellaims_sip_request_duration_seconds` | Histogram | Time from a request of a phone to the final response the P-CSCF sent, by method and status class, the answer of the far end included when the request is forwarded. `INVITE` is left out: its final response waits for the callee. |
| `ellaims_diameter_peer_up` | Gauge | 1 if the connection to a Diameter peer is open, by peer and application. |
| `ellaims_peer_requests_total` | Counter | Requests to the HSS, the policy function, the SMSCs and the MMEs and SMSFs, by interface (`cx`, `rx`, `n5`, `sh`, `s6c`, `sgd`) and result (`success`, `failure`, `error`, `timeout`). |
| `ellaims_peer_request_duration_seconds` | Histogram | Duration of requests to the HSS, the policy function, the SMSCs and the MMEs and SMSFs, by interface. |
| `ellaims_ipsmgw_short_messages_total` | Counter | Short messages the IP-SM-GW carried, by direction (`mo`, `mt`), domain (`smsc` for `mo`; `ims`, `mme`, `sgsn`, `smsf_3gpp`, `smsf_non_3gpp`, `none` for `mt`, the last tried) and result (`success`, `failure`). |
| `ellaims_trunk_info` | Gauge | 1 per SIP trunk, by trunk ID and name. |
| `ellaims_trunk_up` | Gauge | 1 if a SIP trunk answers its OPTIONS keepalives, 0 if it is down, by trunk ID. A trunk without keepalives, disabled, or not probed yet, has no series. |
| `ellaims_trunk_calls_total` | Counter | Calls over a SIP trunk that ended, by the ID of the trunk that gave the final answer, direction (`inbound`, `outbound`) and outcome, as `ellaims_calls_total`. |
| `ellaims_trunk_active_calls` | Gauge | Calls over a SIP trunk in progress, by trunk ID and direction. |
| `ellaims_trunk_failovers_total` | Counter | Calls to a SIP trunk retried on its next server or the next trunk of their route after a transport error, a timeout or a 503, by the ID of the trunk that failed. |
| `ellaims_trunk_media_relays` | Gauge | Media streams that the media relay of SIP trunks relays now, by trunk ID. |
| `ellaims_trunk_media_packets_relayed_total` | Counter | RTP and RTCP packets that the media relay of SIP trunks relayed, by trunk ID, the leg they arrived on (`phone`, `trunk`) and protocol. |
| `ellaims_trunk_media_packets_dropped_total` | Counter | RTP and RTCP packets that the media relay of SIP trunks dropped, by trunk ID, leg and reason: `unexpected_source`, `no_remote`, `no_destination`, `direction`, `malformed`, `send_error`, `policed`. |
| `ellaims_trunk_media_packets_lost_total` | Counter | RTP packets lost before reaching the media relay of SIP trunks, from gaps in sequence numbers, by trunk ID and leg. |
| `ellaims_api_request_duration_seconds` | Histogram | Duration of API requests, by method, route (`/api/v1/users/{email}`) and status code. Requests to `/api/v1/pprof/` and to no route are left out. |
| `ellaims_oidc_logins_total` | Counter | Logins through the identity provider, by outcome: `success`, or a [login error](api/auth.md#errors). |
| `ellaims_database_query_duration_seconds` | Histogram | Duration of database calls, by connection pool: `write`, `read`, `local`. Exemplars: `trace_id`, `span_id`. |
| `ellaims_database_query_errors_total` | Counter | Failed database calls, by connection pool. |
| `ellaims_database_storage_bytes` | Gauge | Size of the databases on disk, by file: `main`, `wal`, `node`, `node_wal`. |
| `ellaims_cluster_failure_tolerance` | Gauge | Voters that can fail without stopping writes. Leader only. |
| `ellaims_cluster_foreign_peers` | Gauge | Nodes behind `cluster.join` that are members of another cluster. |
| `ellaims_cluster_join_requests_total` | Counter | Join and hello requests from other nodes, by result: `admitted`, `rejected`, `unauthorized`, `rate_limited`, `unavailable`. |
| `ellaims_ready_check` | Gauge | 1 when the readiness check passes, 0 when it fails, by [check](api/status.md#checks) (`shutdown`, `member`, `initialized`, `diameter_label`, `drain`, `database`, `leader`). |
| `go_*` | Gauge, Counter, Summary | Go runtime: goroutines, heap, garbage-collection pauses. |
| `process_*` | Gauge, Counter | Process: memory, CPU, open file descriptors, start time. |
| `raft_*` | Gauge, Counter, Summary | [hashicorp/raft](https://github.com/hashicorp/raft) metrics. |

## Traces

Ella IMS exports [OpenTelemetry](https://opentelemetry.io/) traces when `telemetry.enabled` is set in the [configuration file](config_file.md).

| Property | Value |
| --- | --- |
| Export | OTLP over gRPC, without TLS |
| Service | `ella-ims` |
| Sampling | Parent-based, always on for a trace the IMS starts |
| Propagation | W3C Trace Context |
| Error status | HTTP and SIP: a server span from a 5xx response, a client span from a 4xx, except a SIP 401 or 407 challenge. Diameter: a client span from a Result-Code from 3000. |
| SIP trust domain | Trace context is read and sent on SIP only within the trust domain: the IMS's own addresses, and for the I-CSCF, the S-CSCF and the MCPTT AS, `sip.trusted_networks`. It never reaches a phone or a SIP trunk. |

### Spans

| Span | Kind | Description |
| --- | --- | --- |
| `{method} {route}` | Server | A request to the API, except `/api/v1/livez`, `/api/v1/readyz`, `/api/v1/metrics` and `/api/v1/pprof/`. Links to the caller's `traceparent`. |
| `{method} {url.template}` | Client | A request to the PCF on N5. |
| `POST /n5/v1/sessions/{id}/{notify,terminate}` | Server | A notification from the PCF on N5. |
| `Cx/{command}`, `Sh/{command}`, `Rx/{command}`, `S6c/{command}`, `SGd/{command}` | Server, Client | A Diameter request. |
| `{method}`, such as `INVITE` | Server, Client | A SIP transaction, until its final response. A request from a phone or a SIP trunk starts a trace, linked to the INVITE of its call or the REGISTER it renews. Out-of-dialog OPTIONS to and from SIP trunks have no span. |
| `registration expiry`, `media loss`, `media timeout`, … | Internal | Work that starts later than its cause, as on a timer or after a settings change: a new trace, linked to its cause. `media timeout`: the IBCF ends a call whose media stopped, linked to its INVITE. |
| `db.{call}`, such as `db.SaveSet` | Internal | A database call within a trace. |
| `POST /cluster/v1/{propose,join,probe,hello}` | Server, Client | A request between cluster nodes within a trace. `join` and `hello` from a node not yet in the cluster link to the caller's `traceparent`. |
| `raft wait applied` | Internal | A follower applying a write it forwarded to the leader. |
| `raft apply` | Internal | A batch of writes committed to the Raft log, when one of its writes is in a sampled trace. A new trace, linked to the sampled spans of its writes. |

## Logs

| Field | Description |
| --- | --- |
| `trace_id` | Trace ID of the span the event happened in. Absent outside a span. |
| `span_id` | Span ID of the span the event happened in. Absent outside a span. |

### Audit events

Audit events are logs at level `info` with `msg` set to `audit event`.

| Field | Description |
| --- | --- |
| `action` | What was done, such as `auth_login` or `auth_oidc_login`. |
| `actor` | Email of the user who did it. Empty when unknown. |
| `ip` | Address the request came from. |
| `details` | What was done, in words. |

## Profiles

Ella IMS exposes the [net/http/pprof](https://pkg.go.dev/net/http/pprof) API at [`/api/v1/pprof/`](api/pprof.md).
