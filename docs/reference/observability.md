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
| `ellaims_registered_subscribers` | Gauge | Subscribers registered now. |
| `ellaims_registration_attempts_total` | Counter | Registrations and re-registrations, by result: `accept`, `auth_failure`, `reject`. |
| `ellaims_active_calls` | Gauge | Calls in progress. |
| `ellaims_calls_total` | Counter | Calls that ended, by outcome: `answered`, `cancelled`, `busy`, `rejected`, `no_answer`, `unavailable`, `failed`. |
| `ellaims_sip_responses_total` | Counter | Final responses the P-CSCF sent to phones, by method and status class. |
| `ellaims_sip_request_duration_seconds` | Histogram | Time from a request of a phone to the final response the P-CSCF sent, by method and status class, the answer of the far end included when the request is forwarded. `INVITE` is left out: its final response waits for the callee. |
| `ellaims_diameter_peer_up` | Gauge | 1 if the connection to a Diameter peer is open, by peer and application. |
| `ellaims_peer_requests_total` | Counter | Requests to the HSS, the policy function, the SMSCs and the MMEs and SMSFs, by interface (`cx`, `rx`, `n5`, `sh`, `s6c`, `sgd`) and result (`success`, `failure`, `error`, `timeout`). |
| `ellaims_peer_request_duration_seconds` | Histogram | Duration of requests to the HSS, the policy function, the SMSCs and the MMEs and SMSFs, by interface. |
| `ellaims_ipsmgw_short_messages_total` | Counter | Short messages the IP-SM-GW carried, by direction (`mo`, `mt`), domain (`smsc` for `mo`; `ims`, `mme`, `sgsn`, `smsf_3gpp`, `smsf_non_3gpp`, `none` for `mt`, the last tried) and result (`success`, `failure`). |
| `ellaims_api_request_duration_seconds` | Histogram | Duration of API requests, by method, route (`/api/v1/users/{email}`) and status code. Requests to `/api/v1/pprof/` and to no route are left out. |
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
| Error status | HTTP and SIP: a server span from a 5xx response, a client span from a 4xx, except a SIP 401 or 407 challenge. Diameter: a client span from a Result-Code from 3000. |
| SIP trust domain | Trace context is read and sent on SIP only within the trust domain: the IMS's own addresses, and for the I-CSCF, the S-CSCF and the MCPTT AS, `sip.trusted_networks`. It never reaches a phone. |

### Spans

| Span | Kind | Description |
| --- | --- | --- |
| `{method} {route}` | Server | A request to the API, except `/api/v1/livez`, `/api/v1/readyz`, `/api/v1/metrics` and `/api/v1/pprof/`. Links to the caller's `traceparent`. |
| `{method} {url.template}` | Client | A request to the PCF on N5. |
| `POST /n5/v1/sessions/{id}/{notify,terminate}` | Server | A notification from the PCF on N5. |
| `Cx/{command}`, `Sh/{command}`, `Rx/{command}`, `S6c/{command}`, `SGd/{command}` | Server, Client | A Diameter request. |
| `{method}`, such as `INVITE` | Server, Client | A SIP transaction, until its final response. A request from a phone starts a trace, linked to the INVITE of its call or the REGISTER it renews. |
| `registration expiry`, `media loss`, … | Internal | Work that starts later than its cause, as on a timer or after a settings change: a new trace, linked to its cause. |

### Attributes

| Attribute | Values |
| --- | --- |
| `ella.ims.role` | `pcscf`, `icscf`, `scscf`, `mcptt`, `ipsmgw`, or `ims` for work of the IMS as a whole |
| `sip.call_id` | The Call-ID of a SIP span |
| `ella.ims.icid` | The ICID of P-Charging-Vector of a SIP span, which joins it to its [call record](api/call_records.md) |

## Logs

| Field | Description |
| --- | --- |
| `trace_id` | Trace ID of the span the event happened in. Absent outside a span. |
| `span_id` | Span ID of the span the event happened in. Absent outside a span. |

## Profiles

Ella IMS exposes the [net/http/pprof](https://pkg.go.dev/net/http/pprof) API at [`/api/v1/pprof/`](api/pprof.md).
