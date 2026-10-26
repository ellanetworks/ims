---
description: RESTful API reference for viewing the status of Ella IMS.
---

# Status

## Get the Status

This path returns the version of the IMS and what its MCPTT AS runs.

| Method | Path             |
| ------ | ---------------- |
| GET    | `/api/v1/status` |

### Response Fields

| Field                     | Type   | Description |
| ------------------------- | ------ | ----------- |
| `version`                 | string | Version. |
| `revision`                | string | Git commit of the build. Empty in development builds. |
| `mcptt`                   | object | MCPTT AS. Absent if the IMS runs none. |
| `mcptt.mode`              | string | `ims` or `direct`. |
| `mcptt.name`              | string | Server name of the AS in the iFCs of the HSS. |
| `mcptt.participating_psi` | string | Participating PSI. |
| `mcptt.controlling_psi`   | string | Controlling PSI. |
| `mcptt.sip`               | array  | SIP addresses of the AS. Sockets in `direct` mode only. |
| `mcptt.media_control`     | string | Address of the floor control channels. |

### Sample Response

```json
{
    "result": {
        "version": "v0.0.1",
        "revision": "",
        "mcptt": {
            "mode": "ims",
            "name": "sip:mcptt.ims.mnc001.mcc001.3gppnetwork.org",
            "participating_psi": "sip:mcptt-participating@mcptt.ims.mnc001.mcc001.3gppnetwork.org",
            "controlling_psi": "sip:mcptt-controlling@mcptt.ims.mnc001.mcc001.3gppnetwork.org",
            "sip": ["127.0.0.1:5090"],
            "media_control": "127.0.0.1:21000"
        }
    }
}
```
