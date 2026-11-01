---
description: Reference for observability features in Ella IMS.
---

# Observability

Ella IMS supports two observability pillars: Metrics and Traces.

## Metrics

Ella IMS exposes Prometheus metrics at `GET /api/v1/metrics`.

| Metric | Type | Description |
| --- | --- | --- |
| `ellaims_build_info` | Gauge | Always 1. The version and revision of the running build are in its labels. |
| `ellaims_registered_subscribers` | Gauge | Subscribers registered now. |
| `ellaims_registration_attempts_total` | Counter | Registrations and re-registrations, by result: `accept`, `auth_failure`, `reject`. |
| `ellaims_active_calls` | Gauge | Calls in progress. |
| `ellaims_calls_total` | Counter | Calls that ended, by outcome: `answered`, `cancelled`, `busy`, `rejected`, `no_answer`, `unavailable`, `failed`. |
| `ellaims_sip_responses_total` | Counter | Final responses the P-CSCF sent to phones, by method and status class. |
| `ellaims_diameter_peer_up` | Gauge | 1 if the connection to a Diameter peer is open, by peer and application. |
| `ellaims_peer_requests_total` | Counter | Requests to the HSS, the policy function, the SMSCs and the MMEs and SMSFs, by interface (`cx`, `rx`, `n5`, `sh`, `s6c`, `sgd`) and result (`success`, `failure`, `error`, `timeout`). |
| `ellaims_peer_request_duration_seconds` | Histogram | Duration of requests to the HSS, the policy function, the SMSCs and the MMEs and SMSFs, by interface. |
| `ellaims_ipsmgw_short_messages_total` | Counter | Short messages the IP-SM-GW carried, by direction (`mo`, `mt`), domain (`smsc` for `mo`; `ims`, `mme`, `sgsn`, `smsf_3gpp`, `smsf_non_3gpp`, `none` for `mt`, the last tried) and result (`success`, `failure`). |
| `ellaims_database_query_duration_seconds` | Histogram | Duration of database calls, by connection pool: `write`, `read`. |
| `ellaims_database_query_errors_total` | Counter | Failed database calls, by connection pool. |
| `ellaims_database_storage_bytes` | Gauge | Size of the database on disk, by file: `main`, `wal`. |
| `go_*` | Gauge, Counter, Summary | Go runtime: goroutines, heap, garbage-collection pauses. |
| `process_*` | Gauge, Counter | Process: memory, CPU, open file descriptors, start time. |

## Traces

Ella IMS exports [OpenTelemetry](https://opentelemetry.io/) traces when `telemetry.enabled` is set in the [configuration file](config_file.md).

| Property | Value |
| --- | --- |
| Export | OTLP over gRPC, without TLS |
| Service | `ella-ims` |
| Sampling | Parent-based, always on for a trace the IMS starts |
| Propagation | W3C Trace Context |

### Spans

| Span | Kind | Description | `error.type` |
| --- | --- | --- | --- |
| `{method} {route}` | Server | A request to the API, except `/api/v1/livez`, `/api/v1/readyz` and `/api/v1/metrics`. Links to the caller's `traceparent`. | None. A 5xx response has the Error status. |
| `{method} /npcf-policyauthorization/v1/app-sessions…` | Client | A request to the PCF on N5 (TS 29.514), named by the URI template of its resource. Carries `traceparent` to the PCF. | The HTTP status code from 400, or the Go type of the transport error, such as `*net.OpError`. |
| `POST /n5/v1/sessions/{id}/notify`, `POST /n5/v1/sessions/{id}/terminate` | Server | A notification from the PCF on N5. Child of the PCF's trace context. | None. A 5xx response has the Error status. |
| `Cx/{command}`, `Sh/{command}`, `Rx/{command}`, `S6c/{command}`, `SGd/{command}` | Server, Client | A Diameter request. Child of the peer's trace context. | Client: a Result-Code from 3000, `closed`, `not_connected`, `unknown_peer`, `application_unsupported`, `unable_to_deliver`, `loop_detected`, `not_proxiable`, `timeout`, `canceled`, `_OTHER`. Server: `3001`, `3002`, `3004`, `5012`, `overloaded`, `shutting_down`. |
| `{method}`, such as `INVITE` or `REGISTER`; `_OTHER` for an unknown method | Server | A SIP server transaction, until its first final response. Child of the peer's trace context within the trust domain; otherwise a new trace. An `ACK` for a 2xx and a `CANCEL` link to the `INVITE`'s span. | The status code from 500, `timeout`, `transport`, `dropped`, `closed`, `_OTHER`. |
| `{method}` | Client | A SIP client transaction, until its final response. Carries `traceparent` and `tracestate` within the trust domain. A `CANCEL` is a child of its `INVITE`'s span, linked to what cancelled it. | The status code from 400, but 401, 407, and 487 to an `INVITE` the IMS cancelled; `timeout`, `transport`, `dropped`, `closed`, `_OTHER`. |
| `registration expiry`, `challenge expiry`, `security associations expiry`, `third-party registration`, `unregistered profile release`, `reg event resubscribe`, `reg event refresh`, `media loss`, `call end`, `ip-sm-gw hss update`, `delete orphan context`, `apply settings`, `restart core`, `group call maximum duration`, `group call inactivity`, `group call media not revoked`, `session refresh`, `session expiry`, `subscription refresh`, `subscription expiry`, `subscription notify`, `publication refresh`, `publication expiry` | Internal | Work that starts later than its cause: a timer, a queue, the media plane, a settings change. A new trace, linked to its causes. The roles' carry `ella.ims.role`; the `session`, `subscription` and `publication` spans are of the SIP stack. | The Go type of the error, for the roles'. |

### SIP

| Property | Value |
| --- | --- |
| Trust domain | The IMS's own addresses. For the I-CSCF, the S-CSCF and the MCPTT AS, also `sip.trusted_networks`. |
| Trace context | `traceparent` and `tracestate` header fields of requests, read and sent within the trust domain only. Removed from requests sent outside it, and from every response. |

| Attribute | Value |
| --- | --- |
| `ella.ims.role` | `pcscf`, `icscf`, `scscf`, `mcptt`, `ipsmgw`; `ims` for work of the IMS as a whole |
| `sip.request.method` | The method, `_OTHER` if unknown |
| `sip.request.method_original` | An unknown method |
| `sip.response.status_code` | The final status code |
| `sip.call_id` | The Call-ID |
| `ella.ims.icid` | The ICID of P-Charging-Vector |
| `network.protocol.name` | `sip` |
| `network.transport` | `udp`, `tcp` |
| `network.peer.address`, `network.peer.port` | The peer, within the trust domain only |

| Event | Attributes |
| --- | --- |
| `sip.provisional_response` | `sip.response.status_code` |
| `sip.retransmission` | None |

### Logs

| Field | Value |
| --- | --- |
| `trace_id`, `span_id` | The span a log record was written in, if any |
