---
description: RESTful API reference for managing the policy function of the P-CSCF.
---

# Policy

The policy function is the interface the P-CSCF uses for policy control: none, Rx to a PCRF, or N5 to a PCF.

## Get the Policy

This path returns the policy function settings and the policy function running.

| Method | Path             |
| ------ | ---------------- |
| GET    | `/api/v1/policy` |

### Response Fields

| Field                | Type    | Description |
| -------------------- | ------- | ----------- |
| `interface`          | string  | `none`, `rx` or `n5`. |
| `n5`                 | object  | Present for the `n5` interface only. |
| `n5.pcf_uri`         | string  | API root of the PCF, `http[s]://host[:port][/prefix]`. |
| `status`             | object  | Policy function running. Differs from the settings while the IMS applies them. |
| `status.interface`   | string  | `none`, `rx` or `n5`. |
| `status.endpoint`    | string  | `rx` for Rx; the API root of the PCF for N5. |
| `status.notify`      | string  | Address of the N5 notification listener. |
| `status.last`        | object  | Outcome of the last request to the PCF. N5 only. |
| `status.last.at`     | string  | Time of the request (RFC 3339). |
| `status.last.reachable` | boolean | Whether the PCF answered. |
| `status.last.result` | string  | Result of the request. |

### Sample Response

```json
{
    "result": {
        "interface": "rx",
        "status": {
            "interface": "rx",
            "endpoint": "rx"
        }
    }
}
```

## Update the Policy

This path replaces the policy function settings. Calls and registrations after the change use the new policy function.

| Method | Path             |
| ------ | ---------------- |
| PUT    | `/api/v1/policy` |

### Request Body

| Field        | Type   | Required | Description |
| ------------ | ------ | -------- | ----------- |
| `interface`  | string | Yes      | `none`, `rx` or `n5`. `n5` requires `n5` in the configuration file. |
| `n5`         | object | No       | For the `n5` interface only. |
| `n5.pcf_uri` | string | Yes      | `http[s]://host[:port][/prefix]`. `https` requires `n5.tls` in the configuration file, `http` requires none. |

### Sample Request

```json
{
    "interface": "none"
}
```

### Sample Response

```json
{
    "result": {
        "interface": "none",
        "status": {
            "interface": "rx",
            "endpoint": "rx"
        }
    }
}
```
