---
description: RESTful API reference for viewing call records and managing their retention.
---

# Call Records

A call record describes a call the IMS handled, after TS 32.298 §5.1.3.1.

## List Call Records

This path returns the list of call records, most recently requested first.

| Method | Path                   |
| ------ | ---------------------- |
| GET    | `/api/v1/call-records` |

### Query Parameters

| Name       | In    | Type | Default | Allowed | Description |
| ---------- | ----- | ---- | ------- | ------- | ----------- |
| `search`   | query | str  |         |         | Substring of the ICID or of an identity of the caller or the callee. |
| `start`    | query | str  |         |         | Inclusive lower bound of the request time, RFC 3339. Unbounded when omitted. |
| `end`      | query | str  |         |         | Exclusive upper bound of the request time, RFC 3339. Unbounded when omitted. |
| `outcome`  | query | str  |         | `answered`, `cancelled`, `busy`, `rejected`, `no_answer`, `unavailable`, `failed` | Outcome to include. Repeatable. |
| `page`     | query | int  | `1`     | `>= 1`  | 1-based page index. |
| `per_page` | query | int  | `25`    | `1…100` | Number of items per page. |

### Response Fields

| Field               | Type    | Description |
| ------------------- | ------- | ----------- |
| `id`                | int     | Record identifier. |
| `icid`              | string  | icid-value of the P-Charging-Vector (§5.1.3.1.19). |
| `session_id`        | string  | Call-ID of the INVITE (§5.1.3.1.59). |
| `calling_party`     | array   | P-Asserted-Identity values (§5.1.3.1.24). |
| `caller_impi`       | string  | Private user identity of the caller (§5.1.3.1.36). |
| `requested_party`   | string  | Request-URI from the caller (§5.1.3.1.43). |
| `called_party`      | string  | Request-URI after normalisation by the S-CSCF (§5.1.3.1.9). |
| `callee_impi`       | string  | Private user identity of the answering registration (§5.1.3.1.36). |
| `requested_at`      | string  | Reception time of the INVITE (§5.1.3.1.58). |
| `delivery_start_at` | string  | Time of the final response to the caller (§5.1.3.1.55). |
| `delivery_end_at`   | string  | End time of an answered call (§5.1.3.1.54). |
| `sip_status`        | int     | Final status of the INVITE sent to the caller (§5.1.3.1.57). |
| `outcome`           | string  | `answered` (2xx), `cancelled` (487 to a caller CANCEL), `busy` (486, 600), `rejected` (603), `no_answer` (408 after a 180), `unavailable` (480), or `failed` (any other final status). |
| `ended_by`          | string  | `caller`, `callee` or `network`. |
| `alerted`           | boolean | Whether a 180 reached the caller. |
| `media`             | array   | Media types of the accepted m-lines. |
| `in_progress`       | boolean | Whether the call has not ended. |
| `incomplete`        | boolean | Whether the record was closed without the end of the call. |
| `duration_ms`       | int     | Duration of an answered call that ended, in milliseconds. |

A value the IMS did not observe is absent. Times are RFC 3339.

### Sample Response

```json
{
    "result": {
        "items": [
            {
                "id": 42,
                "icid": "1a2b3c4d5e6f7a8b",
                "session_id": "a84b4c76e66710@10.45.0.2",
                "calling_party": [
                    "sip:+15550000001@ims.mnc001.mcc001.3gppnetwork.org",
                    "tel:+15550000001"
                ],
                "caller_impi": "001010000000001@ims.mnc001.mcc001.3gppnetwork.org",
                "requested_party": "tel:15550000002",
                "called_party": "tel:+15550000002",
                "callee_impi": "001010000000002@ims.mnc001.mcc001.3gppnetwork.org",
                "requested_at": "2026-10-26T18:21:10.204Z",
                "delivery_start_at": "2026-10-26T18:21:14.871Z",
                "delivery_end_at": "2026-10-26T18:23:02.330Z",
                "sip_status": 200,
                "outcome": "answered",
                "ended_by": "caller",
                "alerted": true,
                "media": ["audio"],
                "in_progress": false,
                "incomplete": false,
                "duration_ms": 107459
            }
        ],
        "page": 1,
        "per_page": 25,
        "total_count": 1
    }
}
```

## Get a Call Record

This path returns a specific call record.

| Method | Path                        |
| ------ | --------------------------- |
| GET    | `/api/v1/call-records/{id}` |

### Path Parameters

| Name | Type | Description |
| ---- | ---- | ----------- |
| `id` | int  | Record identifier. |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `404` | No record has this identifier. |

### Sample Response

```json
{
    "result": {
        "id": 43,
        "icid": "2b3c4d5e6f7a8b9c",
        "session_id": "b95c5d87f77821@10.45.0.3",
        "calling_party": [
            "sip:+15550000002@ims.mnc001.mcc001.3gppnetwork.org"
        ],
        "caller_impi": "001010000000002@ims.mnc001.mcc001.3gppnetwork.org",
        "requested_party": "tel:+15550000001",
        "called_party": "tel:+15550000001",
        "requested_at": "2026-10-26T18:30:41.019Z",
        "delivery_start_at": "2026-10-26T18:30:50.552Z",
        "sip_status": 486,
        "outcome": "busy",
        "ended_by": "callee",
        "alerted": true,
        "media": [],
        "in_progress": false,
        "incomplete": false
    }
}
```

## Get Call Record Retention Policy

This path returns how long call records are kept.

| Method | Path                             |
| ------ | -------------------------------- |
| GET    | `/api/v1/call-records/retention` |

### Response Fields

| Field  | Type | Description |
| ------ | ---- | ----------- |
| `days` | int  | Number of days call records are kept. |

### Sample Response

```json
{
    "result": {
        "days": 90
    }
}
```

## Update Call Record Retention Policy

This path changes how long call records are kept, from their next pruning.

| Method | Path                             |
| ------ | -------------------------------- |
| PUT    | `/api/v1/call-records/retention` |

### Request Body

| Field  | Type | Required | Description |
| ------ | ---- | -------- | ----------- |
| `days` | int  | Yes      | 1–3650. |

### Sample Request

```json
{
    "days": 30
}
```

### Sample Response

```json
{
    "result": {
        "days": 30
    }
}
```
