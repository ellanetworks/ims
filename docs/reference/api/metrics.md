---
description: RESTful API reference for retrieving Prometheus metrics.
---

# Metrics

## Get Metrics

This path returns the metrics of the IMS in the [Prometheus exposition format](https://prometheus.io/docs/instrumenting/exposition_formats/#text-format-details), negotiated from the request's `Accept` header.

| Method | Path              |
| ------ | ----------------- |
| GET    | `/api/v1/metrics` |

### Sample Response

```
# HELP ellaims_active_calls Calls in progress: attempted and not yet ended.
# TYPE ellaims_active_calls gauge
ellaims_active_calls 0
# HELP ellaims_calls_total Calls that ended, by outcome, as their call records show it.
# TYPE ellaims_calls_total counter
ellaims_calls_total{outcome="answered"} 0
ellaims_calls_total{outcome="busy"} 0
ellaims_calls_total{outcome="cancelled"} 0
ellaims_calls_total{outcome="failed"} 0
ellaims_calls_total{outcome="no_answer"} 0
ellaims_calls_total{outcome="rejected"} 0
ellaims_calls_total{outcome="unavailable"} 0
# HELP ellaims_database_query_errors_total Database calls that failed, by connection pool. A record not found or in conflict is a result, not a failure.
# TYPE ellaims_database_query_errors_total counter
ellaims_database_query_errors_total{pool="read"} 0
ellaims_database_query_errors_total{pool="write"} 0
# HELP ellaims_database_storage_bytes Size of the database on disk, by file: the main file and its write-ahead log.
# TYPE ellaims_database_storage_bytes gauge
ellaims_database_storage_bytes{file="main"} 4096
ellaims_database_storage_bytes{file="wal"} 646872
```
