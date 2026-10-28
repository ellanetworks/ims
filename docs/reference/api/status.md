---
description: RESTful API reference for viewing the status of Ella IMS.
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
