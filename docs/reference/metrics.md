---
description: Reference of the Prometheus metrics Ella IMS exposes.
---

# Metrics

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
