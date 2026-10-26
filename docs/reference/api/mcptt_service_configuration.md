---
description: RESTful API reference for managing the MCPTT service configuration.
---

# MCPTT Service Configuration

The service configuration of the MCPTT organisation (TS 24.484 §8.4). The `/api/v1/mcptt` paths answer `404` when the IMS does not run the MCPTT AS.

## Get the Service Configuration

This path returns the service configuration.

| Method | Path                                    |
| ------ | --------------------------------------- |
| GET    | `/api/v1/mcptt/service-configuration`   |

### Response Fields

| Field                    | Type   | Description |
| ------------------------ | ------ | ----------- |
| `domain`                 | string | Domain of the organisation, of its MCPTT IDs and group IDs, and the IOI of its charging. The IMS home domain until set. |
| `transmit_time_limit_ms` | int    | How long a participant may talk at a time, T2 (TS 24.380 Table 11.1.3-1). Absent if not set, in which case it is 30 s. |

### Sample Response

```json
{
    "result": {
        "domain": "ims.mnc001.mcc001.3gppnetwork.org"
    }
}
```

## Update the Service Configuration

This path replaces the service configuration.

| Method | Path                                    |
| ------ | --------------------------------------- |
| PUT    | `/api/v1/mcptt/service-configuration`   |

### Request Body

| Field                    | Type   | Required | Description |
| ------------------------ | ------ | -------- | ----------- |
| `domain`                 | string | Yes      | Domain of the organisation. |
| `transmit_time_limit_ms` | int    | No       | At least 1. |

### Sample Request

```json
{
    "domain": "mcptt.example.com",
    "transmit_time_limit_ms": 30000
}
```

### Sample Response

```json
{
    "result": {
        "domain": "mcptt.example.com",
        "transmit_time_limit_ms": 30000
    }
}
```
