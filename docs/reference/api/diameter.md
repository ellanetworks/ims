---
description: RESTful API reference for managing Diameter peers and routes.
---

# Diameter

Diameter peers are the HSSs, SLFs, PCRFs, DRAs, SMSCs, MMEs and SMSFs Ella IMS connects to. Routes set the realm the requests of each routed application go to.

## Get the Diameter Identity

This path returns the Diameter identity of the IMS, derived from the operator settings.

| Method | Path               |
| ------ | ------------------ |
| GET    | `/api/v1/diameter` |

### Response Fields

| Field   | Type   | Description |
| ------- | ------ | ----------- |
| `host`  | string | Origin-Host of the IMS. |
| `realm` | string | Origin-Realm of the IMS. |

### Sample Response

```json
{
    "result": {
        "host": "ims.ims.mnc001.mcc001.3gppnetwork.org",
        "realm": "ims.mnc001.mcc001.3gppnetwork.org"
    }
}
```

## List Diameter Peers

This path returns the list of Diameter peers, in the order they were created in.

| Method | Path                     |
| ------ | ------------------------ |
| GET    | `/api/v1/diameter/peers` |

### Response Fields

| Field                   | Type   | Description |
| ----------------------- | ------ | ----------- |
| `id`                    | string | Peer identifier, a UUIDv7. |
| `host`                  | string | DiameterIdentity of the peer, which must match the Origin-Host of its capabilities exchange. Empty takes the Origin-Host the peer gives. |
| `address`               | string | IPv4 or IPv6 address of the peer. |
| `port`                  | int    | Port of the peer. |
| `transport`             | string | `tcp` or `sctp`. |
| `applications`          | array  | Applications the peer serves: `cx` for an HSS or an SLF, and `sh` and `s6c` for its IP-SM-GW functions; `rx` for a PCRF or a DRA; `sgd` for an SMSC, an MME or an SMSF. |
| `priority`              | int    | Requests try the peers serving their application by priority, lowest first, and share them between peers of the same priority. SMSCs of the same service centre are tried by priority. |
| `service_centres`       | array  | Service centre addresses of an SMSC, E.164 numbers without `+`. Absent if none. |
| `status`                | object | Connection to the peer. |
| `status.state`          | string | `down`, `connecting`, `open`, `suspect`, `reopen` or `closing`. `down` until the IMS has tried the peer. |
| `status.since`          | string | Time of the last state change (RFC 3339). |
| `status.remote_address` | string | Remote address of the connection. |
| `status.host`           | string | Origin-Host the peer gave in its last capabilities exchange, else the configured host. |
| `status.realm`          | string | Origin-Realm the peer gave in its last capabilities exchange. |
| `status.error`          | string | Why the last connection to the peer failed. |

### Sample Response

```json
{
    "result": {
        "items": [
            {
                "id": "01a1796b-90b3-74bc-b148-bdc28999206e",
                "host": "hss.epc.mnc001.mcc001.3gppnetwork.org",
                "address": "127.0.0.1",
                "port": 3869,
                "transport": "tcp",
                "applications": ["cx"],
                "priority": 10,
                "status": {
                    "state": "down",
                    "since": "2026-10-26T18:14:14.963Z",
                    "host": "hss.epc.mnc001.mcc001.3gppnetwork.org",
                    "error": "tcp: dial tcp 127.0.0.1:0->127.0.0.1:3869: connect: connection refused"
                }
            },
            {
                "id": "01a1796b-90bb-7a57-8972-269dab84d5af",
                "host": "",
                "address": "127.0.0.1",
                "port": 3870,
                "transport": "tcp",
                "applications": ["rx"],
                "priority": 10,
                "status": {
                    "state": "down",
                    "since": "2026-10-26T18:14:14.971Z",
                    "error": "tcp: dial tcp 127.0.0.1:0->127.0.0.1:3870: connect: connection refused"
                }
            }
        ]
    }
}
```

## Get a Diameter Peer

This path returns the details of a specific Diameter peer.

| Method | Path                          |
| ------ | ----------------------------- |
| GET    | `/api/v1/diameter/peers/{id}` |

### Path Parameters

| Name | Type   | Description |
| ---- | ------ | ----------- |
| `id` | string | Peer identifier. |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `404` | No peer has this identifier. |

### Sample Response

```json
{
    "result": {
        "id": "01a1796b-90b3-74bc-b148-bdc28999206e",
        "host": "hss.epc.mnc001.mcc001.3gppnetwork.org",
        "address": "127.0.0.1",
        "port": 3869,
        "transport": "tcp",
        "applications": ["cx"],
        "priority": 10,
        "status": {
            "state": "down",
            "since": "2026-10-26T18:14:14.963Z",
            "host": "hss.epc.mnc001.mcc001.3gppnetwork.org",
            "error": "tcp: dial tcp 127.0.0.1:0->127.0.0.1:3869: connect: connection refused"
        }
    }
}
```

## Create a Diameter Peer

This path creates a Diameter peer, which the IMS then connects to.

| Method | Path                     |
| ------ | ------------------------ |
| POST   | `/api/v1/diameter/peers` |

### Request Body

| Field             | Type   | Required | Description |
| ----------------- | ------ | -------- | ----------- |
| `host`            | string | No       | Domain name. Unique among peers. |
| `address`         | string | Yes      | Specific IPv4 or IPv6 address. |
| `port`            | int    | No       | 1–65535. Default `3868`. |
| `transport`       | string | No       | `tcp` or `sctp`. Default `tcp`. |
| `applications`    | array  | Yes      | At least one of `cx`, `rx`, `sh`, `s6c` and `sgd`, each once. |
| `priority`        | int    | No       | 0–65535. Default `10`. |
| `service_centres` | array  | No       | E.164 numbers of 1 to 15 digits, without `+`, each once. Requires `sgd` in `applications` and a `host`. |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `409` | Another peer has the same host, or the same address and transport without a host. |

### Sample Request

```json
{
    "host": "hss.epc.mnc001.mcc001.3gppnetwork.org",
    "address": "127.0.0.1",
    "port": 3869,
    "transport": "tcp",
    "applications": ["cx"],
    "priority": 10
}
```

### Sample Response

```json
{
    "result": {
        "id": "01a1796b-90b3-74bc-b148-bdc28999206e",
        "host": "hss.epc.mnc001.mcc001.3gppnetwork.org",
        "address": "127.0.0.1",
        "port": 3869,
        "transport": "tcp",
        "applications": ["cx"],
        "priority": 10,
        "status": {
            "state": "down"
        }
    }
}
```

## Update a Diameter Peer

This path replaces a Diameter peer, which the IMS then reconnects to. The request body is that of [Create a Diameter Peer](#create-a-diameter-peer).

| Method | Path                          |
| ------ | ----------------------------- |
| PUT    | `/api/v1/diameter/peers/{id}` |

### Path Parameters

| Name | Type   | Description |
| ---- | ------ | ----------- |
| `id` | string | Peer identifier. |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `404` | No peer has this identifier. |
| `409` | Another peer has the same host, or the same address and transport without a host; or the policy interface is `rx` and no other peer serves `rx`. |

### Sample Request

```json
{
    "host": "hss.epc.mnc001.mcc001.3gppnetwork.org",
    "address": "127.0.0.1",
    "port": 3869,
    "transport": "tcp",
    "applications": ["cx"],
    "priority": 5
}
```

### Sample Response

```json
{
    "result": {
        "id": "01a1796b-90b3-74bc-b148-bdc28999206e",
        "host": "hss.epc.mnc001.mcc001.3gppnetwork.org",
        "address": "127.0.0.1",
        "port": 3869,
        "transport": "tcp",
        "applications": ["cx"],
        "priority": 5,
        "status": {
            "state": "down",
            "since": "2026-10-26T18:14:14.963Z",
            "host": "hss.epc.mnc001.mcc001.3gppnetwork.org",
            "error": "tcp: dial tcp 127.0.0.1:0->127.0.0.1:3869: connect: connection refused"
        }
    }
}
```

## Delete a Diameter Peer

This path deletes a Diameter peer.

| Method | Path                          |
| ------ | ----------------------------- |
| DELETE | `/api/v1/diameter/peers/{id}` |

### Path Parameters

| Name | Type   | Description |
| ---- | ------ | ----------- |
| `id` | string | Peer identifier. |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `404` | No peer has this identifier. |
| `409` | The policy interface is `rx` and no other peer serves `rx`. |

### Sample Response

```json
{
    "result": {
        "message": "Diameter peer deleted"
    }
}
```

## List Diameter Routes

This path returns the route of each routed application: `cx`, `rx`, `sh`, then `s6c`.

| Method | Path                      |
| ------ | ------------------------- |
| GET    | `/api/v1/diameter/routes` |

### Response Fields

| Field               | Type   | Description |
| ------------------- | ------ | ----------- |
| `application`       | string | `cx`, `rx`, `sh` or `s6c`. |
| `realm`             | string | Configured realm: of the HSS for `cx`, `sh` and `s6c`, of the PCRF for `rx`. Empty if not set. |
| `destination_realm` | string | Destination-Realm of the requests. If `realm` is empty: the `cx` realm for `sh` and `s6c`, the home domain otherwise. |
| `peers`             | array  | Peers serving the application, in the order requests try them, each with its `id`, `host`, `priority` and `status`. |

### Sample Response

```json
{
    "result": {
        "items": [
            {
                "application": "cx",
                "realm": "epc.mnc001.mcc001.3gppnetwork.org",
                "destination_realm": "epc.mnc001.mcc001.3gppnetwork.org",
                "peers": [
                    {
                        "id": "01a1796b-90b3-74bc-b148-bdc28999206e",
                        "host": "hss.epc.mnc001.mcc001.3gppnetwork.org",
                        "priority": 5,
                        "status": {
                            "state": "down",
                            "since": "2026-10-26T18:14:21.052Z",
                            "host": "hss.epc.mnc001.mcc001.3gppnetwork.org",
                            "error": "tcp: dial tcp 127.0.0.1:0->127.0.0.1:3869: connect: connection refused"
                        }
                    }
                ]
            },
            {
                "application": "rx",
                "realm": "",
                "destination_realm": "ims.mnc001.mcc001.3gppnetwork.org",
                "peers": [
                    {
                        "id": "01a1796b-90bb-7a57-8972-269dab84d5af",
                        "host": "",
                        "priority": 10,
                        "status": {
                            "state": "down",
                            "since": "2026-10-26T18:14:14.971Z",
                            "error": "tcp: dial tcp 127.0.0.1:0->127.0.0.1:3870: connect: connection refused"
                        }
                    }
                ]
            }
        ]
    }
}
```

## Get a Diameter Route

This path returns the route of an application.

| Method | Path                                    |
| ------ | --------------------------------------- |
| GET    | `/api/v1/diameter/routes/{application}` |

### Path Parameters

| Name          | Type   | Description |
| ------------- | ------ | ----------- |
| `application` | string | `cx`, `rx`, `sh` or `s6c`. |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `404` | The application is not routed. |

### Sample Response

```json
{
    "result": {
        "application": "rx",
        "realm": "",
        "destination_realm": "ims.mnc001.mcc001.3gppnetwork.org",
        "peers": [
            {
                "id": "01a1796b-90bb-7a57-8972-269dab84d5af",
                "host": "",
                "priority": 10,
                "status": {
                    "state": "down",
                    "since": "2026-10-26T18:14:14.971Z",
                    "error": "tcp: dial tcp 127.0.0.1:0->127.0.0.1:3870: connect: connection refused"
                }
            }
        ]
    }
}
```

## Update a Diameter Route

This path sets the realm of an application's requests. The next request goes to the new realm.

| Method | Path                                    |
| ------ | --------------------------------------- |
| PUT    | `/api/v1/diameter/routes/{application}` |

### Path Parameters

| Name          | Type   | Description |
| ------------- | ------ | ----------- |
| `application` | string | `cx`, `rx`, `sh` or `s6c`. |

### Request Body

| Field   | Type   | Required | Description |
| ------- | ------ | -------- | ----------- |
| `realm` | string | No       | Domain name. Absent or empty restores the default. |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `404` | The application is not routed. |

### Sample Request

```json
{
    "realm": "epc.mnc001.mcc001.3gppnetwork.org"
}
```

### Sample Response

```json
{
    "result": {
        "application": "cx",
        "realm": "epc.mnc001.mcc001.3gppnetwork.org",
        "destination_realm": "epc.mnc001.mcc001.3gppnetwork.org",
        "peers": [
            {
                "id": "01a1796b-90b3-74bc-b148-bdc28999206e",
                "host": "hss.epc.mnc001.mcc001.3gppnetwork.org",
                "priority": 10,
                "status": {
                    "state": "down",
                    "since": "2026-10-26T18:14:14.963Z",
                    "host": "hss.epc.mnc001.mcc001.3gppnetwork.org",
                    "error": "tcp: dial tcp 127.0.0.1:0->127.0.0.1:3869: connect: connection refused"
                }
            }
        ]
    }
}
```
