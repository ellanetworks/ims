---
description: RESTful API reference for viewing the status and health of Ella IMS.
---

# Status

## Get the Status

This path returns the version of the IMS, and whether it has been initialized. It takes no token.

| Method | Path             |
| ------ | ---------------- |
| GET    | `/api/v1/status` |

### Response Fields

| Field         | Type    | Description |
| ------------- | ------- | ----------- |
| `version`     | string  | Version. |
| `revision`    | string  | Git commit of the build. Empty in development builds. |
| `initialized` | boolean | Whether the first user exists. Until then, [initialize](initialize.md) Ella IMS. |

### Sample Response

```json
{
    "result": {
        "version": "v0.0.1",
        "revision": "",
        "initialized": true
    }
}
```

## Check Liveness

This path answers while the process serves HTTP. It takes no token.

| Method | Path            |
| ------ | --------------- |
| GET    | `/api/v1/livez` |

It answers `204 No Content`.

## Check Readiness

This path returns whether the IMS can serve requests: its database answers, and it is not shutting down. It takes no token.

| Method | Path             |
| ------ | ---------------- |
| GET    | `/api/v1/readyz` |

It answers `204 No Content` when ready, and `503 Service Unavailable` otherwise:

```json
{
    "error": "Not ready"
}
```
