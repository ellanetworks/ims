---
description: RESTful API reference for managing SIP trunks and SIP routes.
---

# SIP Trunks

SIP trunks connect Ella IMS to carriers, for calls to and from numbers outside its HSS. Ella IMS peers with each carrier by address, in the Static mode of SIPconnect 2.0, without registering. SIP routes pick the trunks a call to an E.164 number tries, by the number's prefix.

## List SIP Trunks

This path returns the list of SIP trunks, ordered by identifier.

| Method | Path                  |
| ------ | --------------------- |
| GET    | `/api/v1/sip-trunks` |

### Response Fields

| Field                  | Type    | Description |
| ---------------------- | ------- | ----------- |
| `id`                   | string  | Trunk identifier, a UUIDv7. |
| `name`                 | string  | Name of the trunk. |
| `addresses`            | array   | Where requests to the carrier go, in order, each with its `host`, and its `port` and `transport` if set. |
| `addresses[].host`     | string  | Domain name, or IPv4 or IPv6 address. |
| `addresses[].port`     | int     | Port. Absent if not set. |
| `addresses[].transport` | string | `udp`, `tcp` or `tls`. Absent if not set. |
| `inbound_cidrs`        | array   | Networks that requests from the carrier come from. |
| `domain`               | string  | Domain name or IP address of the carrier, the host of the Request-URI of requests to the carrier. |
| `trusted`              | boolean | Whether the carrier is in the trust domain of the IMS, by an interconnect agreement. |
| `keepalive_interval`   | int     | Seconds between the OPTIONS requests that check the carrier is up. `0` if the IMS sends none. |
| `enabled`              | boolean | Whether routes may use the trunk. |
| `tls`                  | object  | TLS with the carrier. See [TLS](#tls). |
| `tls.required`         | boolean | Whether the carrier must use TLS. |
| `tls.require_client_certificate` | boolean | Whether the carrier must present a certificate when it opens a TLS connection. |
| `tls.ca_certificates`  | string  | PEM-encoded CA certificates. Empty if none. |
| `tls.pinned_certificates` | string | PEM-encoded pinned certificates. Empty if none. |
| `tls.pinned_fingerprints` | array | Pinned SHA-256 fingerprints, as upper-case hex bytes separated by colons. |
| `tls.identities`       | array   | Domain names one of which the carrier's certificate must hold. |
| `tls.ca_certificate_details` | array | One entry per CA certificate, in order. See [Certificate Details](#certificate-details). |
| `tls.pinned_certificate_details` | array | One entry per pinned certificate, in order. See [Certificate Details](#certificate-details). |
| `status`               | object  | Live state of the trunk. |
| `status.state`         | string  | Whether the carrier answers OPTIONS requests. `up`: it answers, and routes use the trunk. `down`: it does not, and routes skip the trunk. `unknown`: not probed, because `keepalive_interval` is `0` or the first OPTIONS request has not been answered yet; routes use the trunk. |
| `status.since`         | string  | Time of the last state change (RFC 3339). Absent while `unknown`. |

### Certificate Details

| Field                | Type    | Description |
| -------------------- | ------- | ----------- |
| `subject`            | string  | Subject distinguished name. |
| `issuer`             | string  | Issuer distinguished name. |
| `dns_names`          | array   | DNS names of the subjectAltName. |
| `uris`               | array   | URIs of the subjectAltName. |
| `ip_addresses`       | array   | IP addresses of the subjectAltName. |
| `identities`         | array   | Identities that Ella IMS matches against `tls.identities`: the hosts of the `sip` URIs of the subjectAltName, else its DNS names, else the common name if the certificate has no subjectAltName. |
| `ca`                 | boolean | Whether the certificate is a CA certificate. |
| `not_before`         | string  | Start of validity (RFC 3339). |
| `not_after`          | string  | End of validity (RFC 3339). |
| `sha256_fingerprint` | string  | SHA-256 fingerprint of the certificate, as upper-case hex bytes separated by colons. |

### Sample Response

```json
{
    "result": {
        "items": [
            {
                "id": "019a5a3e-1b2c-7d4e-8f60-1a2b3c4d5e6f",
                "name": "Telnyx",
                "addresses": [
                    {
                        "host": "sip.telnyx.com",
                        "port": 5060,
                        "transport": "tcp"
                    }
                ],
                "inbound_cidrs": ["192.76.120.10/32", "64.16.250.10/32"],
                "domain": "sip.telnyx.com",
                "trusted": true,
                "keepalive_interval": 30,
                "enabled": true,
                "tls": {
                    "required": false,
                    "require_client_certificate": false,
                    "ca_certificates": "",
                    "pinned_certificates": "",
                    "pinned_fingerprints": [],
                    "identities": [],
                    "ca_certificate_details": [],
                    "pinned_certificate_details": []
                },
                "status": {
                    "state": "unknown"
                }
            }
        ]
    }
}
```

## Get a SIP Trunk

This path returns the details of a specific SIP trunk.

| Method | Path                       |
| ------ | -------------------------- |
| GET    | `/api/v1/sip-trunks/{id}` |

### Path Parameters

| Name | Type   | Description |
| ---- | ------ | ----------- |
| `id` | string | Trunk identifier. |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `404` | No trunk has this identifier. |

### Sample Response

```json
{
    "result": {
        "id": "019a5a3e-1b2c-7d4e-8f60-1a2b3c4d5e6f",
        "name": "Telnyx",
        "addresses": [
            {
                "host": "sip.telnyx.com",
                "port": 5060,
                "transport": "tcp"
            }
        ],
        "inbound_cidrs": ["192.76.120.10/32", "64.16.250.10/32"],
        "domain": "sip.telnyx.com",
        "trusted": true,
        "keepalive_interval": 30,
        "enabled": true,
        "tls": {
            "required": false,
            "require_client_certificate": false,
            "ca_certificates": "",
            "pinned_certificates": "",
            "pinned_fingerprints": [],
            "identities": [],
            "ca_certificate_details": [],
            "pinned_certificate_details": []
        },
        "status": {
            "state": "unknown"
        }
    }
}
```

## Create a SIP Trunk

This path creates a SIP trunk.

| Method | Path                  |
| ------ | --------------------- |
| POST   | `/api/v1/sip-trunks` |

### Request Body

| Field                   | Type    | Required | Description |
| ----------------------- | ------- | -------- | ----------- |
| `name`                  | string  | Yes      | 1 to 64 printable characters, without leading or trailing spaces. Unique among trunks, ignoring case. |
| `addresses`             | array   | Yes      | At least one address, each once. A domain name is preferred over an IP address, as SIPconnect 2.0 recommends: the carrier can then move its servers without a change here. See [Addresses and DNS](#addresses-and-dns). |
| `addresses[].host`      | string  | Yes      | Domain name, or specific IPv4 or IPv6 address. |
| `addresses[].port`      | int     | No       | 1–65535. Absent lets DNS decide, or takes the default port of the transport for an IP address. |
| `addresses[].transport` | string  | No       | `udp`, `tcp` or `tls`. Absent lets DNS decide. `tls` needs `tls.ca_certificates`, `tls.pinned_certificates` or `tls.pinned_fingerprints`. Not `udp` on a trusted trunk. |
| `inbound_cidrs`         | array   | No       | Networks, such as `192.76.120.10/32`, or single addresses, such as `192.76.120.10`. No two networks may overlap, in this trunk or across trunks, and none may be `0.0.0.0/0` or `::/0`. Absent or empty accepts no requests from the carrier. |
| `domain`                | string  | Yes      | Domain name of the carrier, or its specific IPv4 or IPv6 address, without brackets. |
| `trusted`               | boolean | No       | Default `false`. A carrier that needs P-Asserted-Identity on every call must be trusted. A trusted trunk never uses UDP: no address may have `udp` as transport, and an address without a transport is reached over TCP or TLS only. |
| `keepalive_interval`    | int     | No       | 5–3600 seconds, or `0` to send no OPTIONS requests. Default `30`. |
| `enabled`               | boolean | Yes      | Whether routes may use the trunk. |
| `tls`                   | object  | No       | TLS with the carrier. Absent for a trunk without TLS. See [TLS](#tls). |
| `tls.required`          | boolean | No       | Default `false`. Whether the carrier must use TLS: every address must then have `tls` as transport, and requests over UDP or TCP from the inbound networks are refused. |
| `tls.require_client_certificate` | boolean | No | Default `false`. Whether the carrier must present a certificate when it opens a TLS connection to Ella IMS, for mutual authentication. |
| `tls.ca_certificates`   | string  | No       | PEM-encoded CA certificates, at most 32, that the carrier's certificate must chain to. Requires `tls.identities`. |
| `tls.pinned_certificates` | string | No      | PEM-encoded certificates, at most 32, that the carrier presents itself. |
| `tls.pinned_fingerprints` | array | No       | SHA-256 fingerprints, at most 32, of certificates that the carrier presents itself, as 32 hex bytes, with or without colons. |
| `tls.identities`        | array   | No       | Domain names, at most 16, one of which the carrier's certificate must hold. |

### Addresses and DNS

What an address leaves out, DNS decides, following RFC 3263:

| `host`      | `transport` | `port` | Requests go to |
| ----------- | ----------- | ------ | -------------- |
| Domain name | —           | —      | The transport, port and addresses of the carrier's DNS NAPTR records, then SRV records, then A and AAAA records. |
| Domain name | Set         | —      | The port and addresses of the SRV records for that transport, else the A and AAAA records at the default port. |
| Domain name | Set or —    | Set    | The addresses of the A and AAAA records, at that port, over that transport, else UDP. |
| IP address  | Set or —    | Set or — | That address, at that port, else the default port of the transport, over that transport, else UDP. |

The default port is `5060` for `udp` and `tcp`, and `5061` for `tls` (RFC 3261 section 19.1.1).

A trusted trunk never uses UDP: DNS records for UDP are skipped, and TCP replaces UDP where the table says "else UDP".

### TLS

Ella IMS authenticates the carrier over TLS as SIPconnect 2.0 section 17.2 has it, by any of:

- a CA certificate that the carrier's certificate chains to, in `tls.ca_certificates`; `tls.identities` must then list the carrier's domain names;
- the carrier's own certificate, in `tls.pinned_certificates`;
- the SHA-256 fingerprint of the carrier's own certificate, in `tls.pinned_fingerprints`.

The carrier's certificate must hold one of `tls.identities` in its subjectAltName, as the host of a `sip` URI or as a DNS name, or, if it has no subjectAltName, as its common name (SIPconnect 2.0 section 8.1). Without `tls.identities`, the identity is the domain name of the address a request goes to; for an IP address, the pinned certificate or fingerprint alone authenticates the carrier.

Certificates must parse, CA certificates must be CA certificates, and no list may hold the same entry twice. A certificate that has expired is refused when it is added; one that expires after it was added stays until it is replaced. The request holds no private key: Ella IMS's own certificate and key are in the configuration file.

`tls.required` makes the carrier use TLS in both directions (SIPconnect 2.0 section 17.2): every address must have `tls` as transport, and requests from the inbound networks over UDP or TCP are refused.

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `409` | Another trunk has the same name, or an inbound network that overlaps one of this trunk's. |

### Sample Request

```json
{
    "name": "Telnyx",
    "addresses": [
        {
            "host": "sip.telnyx.com",
            "port": 5060,
            "transport": "tcp"
        }
    ],
    "inbound_cidrs": ["192.76.120.10/32", "64.16.250.10/32"],
    "domain": "sip.telnyx.com",
    "trusted": true,
    "enabled": true
}
```

### Sample Response

```json
{
    "result": {
        "id": "019a5a3e-1b2c-7d4e-8f60-1a2b3c4d5e6f",
        "name": "Telnyx",
        "addresses": [
            {
                "host": "sip.telnyx.com",
                "port": 5060,
                "transport": "tcp"
            }
        ],
        "inbound_cidrs": ["192.76.120.10/32", "64.16.250.10/32"],
        "domain": "sip.telnyx.com",
        "trusted": true,
        "keepalive_interval": 30,
        "enabled": true,
        "tls": {
            "required": false,
            "require_client_certificate": false,
            "ca_certificates": "",
            "pinned_certificates": "",
            "pinned_fingerprints": [],
            "identities": [],
            "ca_certificate_details": [],
            "pinned_certificate_details": []
        },
        "status": {
            "state": "unknown"
        }
    }
}
```

### Sample Request with TLS

```json
{
    "name": "Telnyx",
    "addresses": [
        {
            "host": "sip.telnyx.com",
            "transport": "tls"
        }
    ],
    "inbound_cidrs": ["192.76.120.10/32", "64.16.250.10/32"],
    "domain": "sip.telnyx.com",
    "trusted": true,
    "enabled": true,
    "tls": {
        "required": true,
        "ca_certificates": "-----BEGIN CERTIFICATE-----\nMIIBczCCARigAwIBAgII...\n-----END CERTIFICATE-----\n",
        "identities": ["sip.telnyx.com"]
    }
}
```

The `tls` object of its response:

```json
{
    "required": true,
    "require_client_certificate": false,
    "ca_certificates": "-----BEGIN CERTIFICATE-----\nMIIBczCCARigAwIBAgII...\n-----END CERTIFICATE-----\n",
    "pinned_certificates": "",
    "pinned_fingerprints": [],
    "identities": ["sip.telnyx.com"],
    "ca_certificate_details": [
        {
            "subject": "CN=Telnyx CA,O=Telnyx",
            "issuer": "CN=Telnyx CA,O=Telnyx",
            "dns_names": [],
            "uris": [],
            "ip_addresses": [],
            "identities": [],
            "ca": true,
            "not_before": "2026-01-01T00:00:00.000Z",
            "not_after": "2036-01-01T00:00:00.000Z",
            "sha256_fingerprint": "2C:54:DF:66:29:19:96:C5:C8:A5:E8:4B:7A:32:FE:71:02:38:11:62:D4:D9:2E:2C:A1:C4:E7:8C:0D:D5:B1:C5"
        }
    ],
    "pinned_certificate_details": []
}
```

## Update a SIP Trunk

This path replaces a SIP trunk. The request body is that of [Create a SIP Trunk](#create-a-sip-trunk): an optional field left out takes its default, and `enabled` is required.

| Method | Path                       |
| ------ | -------------------------- |
| PUT    | `/api/v1/sip-trunks/{id}` |

### Path Parameters

| Name | Type   | Description |
| ---- | ------ | ----------- |
| `id` | string | Trunk identifier. |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `404` | No trunk has this identifier. |
| `409` | Another trunk has the same name, or an inbound network that overlaps one of this trunk's. |

### Sample Request

```json
{
    "name": "Telnyx",
    "addresses": [
        {
            "host": "sip.telnyx.com",
            "port": 5060,
            "transport": "tcp"
        }
    ],
    "inbound_cidrs": ["192.76.120.10/32", "64.16.250.10/32"],
    "domain": "sip.telnyx.com",
    "trusted": true,
    "enabled": false
}
```

### Sample Response

```json
{
    "result": {
        "id": "019a5a3e-1b2c-7d4e-8f60-1a2b3c4d5e6f",
        "name": "Telnyx",
        "addresses": [
            {
                "host": "sip.telnyx.com",
                "port": 5060,
                "transport": "tcp"
            }
        ],
        "inbound_cidrs": ["192.76.120.10/32", "64.16.250.10/32"],
        "domain": "sip.telnyx.com",
        "trusted": true,
        "keepalive_interval": 30,
        "enabled": false,
        "tls": {
            "required": false,
            "require_client_certificate": false,
            "ca_certificates": "",
            "pinned_certificates": "",
            "pinned_fingerprints": [],
            "identities": [],
            "ca_certificate_details": [],
            "pinned_certificate_details": []
        },
        "status": {
            "state": "unknown"
        }
    }
}
```

## Delete a SIP Trunk

This path deletes a SIP trunk that no route uses.

| Method | Path                       |
| ------ | -------------------------- |
| DELETE | `/api/v1/sip-trunks/{id}` |

### Path Parameters

| Name | Type   | Description |
| ---- | ------ | ----------- |
| `id` | string | Trunk identifier. |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `404` | No trunk has this identifier. |
| `409` | A route uses the trunk. |

### Sample Response

```json
{
    "result": {
        "message": "SIP trunk deleted"
    }
}
```

## List SIP Routes

This path returns the list of SIP routes, ordered by identifier. A call to an E.164 number takes the route of the longest prefix the number starts with, else the default route, and tries the route's trunks in order.

| Method | Path                  |
| ------ | --------------------- |
| GET    | `/api/v1/sip-routes` |

### Response Fields

| Field    | Type   | Description |
| -------- | ------ | ----------- |
| `id`     | string | Route identifier, a UUIDv7. |
| `prefix` | string | Start of the E.164 numbers the route takes, with `+`. Empty for the default route. |
| `trunks` | array  | Identifiers of the trunks of the route, in the order calls try them. |

### Sample Response

```json
{
    "result": {
        "items": [
            {
                "id": "019a5a3f-0c1d-7e2f-9a3b-4c5d6e7f8091",
                "prefix": "",
                "trunks": ["019a5a3e-1b2c-7d4e-8f60-1a2b3c4d5e6f"]
            },
            {
                "id": "019a5a3f-2a3b-7c4d-8e5f-60718293a4b5",
                "prefix": "+1",
                "trunks": ["019a5a3e-1b2c-7d4e-8f60-1a2b3c4d5e6f", "019a5a3e-5f6a-7b8c-9d0e-1f2a3b4c5d6e"]
            }
        ]
    }
}
```

## Get a SIP Route

This path returns the details of a specific SIP route.

| Method | Path                       |
| ------ | -------------------------- |
| GET    | `/api/v1/sip-routes/{id}` |

### Path Parameters

| Name | Type   | Description |
| ---- | ------ | ----------- |
| `id` | string | Route identifier. |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `404` | No route has this identifier. |

### Sample Response

```json
{
    "result": {
        "id": "019a5a3f-2a3b-7c4d-8e5f-60718293a4b5",
        "prefix": "+1",
        "trunks": ["019a5a3e-1b2c-7d4e-8f60-1a2b3c4d5e6f", "019a5a3e-5f6a-7b8c-9d0e-1f2a3b4c5d6e"]
    }
}
```

## Create a SIP Route

This path creates a SIP route.

| Method | Path                  |
| ------ | --------------------- |
| POST   | `/api/v1/sip-routes` |

### Request Body

| Field    | Type   | Required | Description |
| -------- | ------ | -------- | ----------- |
| `prefix` | string | No       | `+` followed by 1 to 15 digits, not starting with `0`. Absent or empty for the default route. Unique among routes. |
| `trunks` | array  | Yes      | Identifiers of existing trunks, at least one, each once. |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `400` | A trunk does not exist. |
| `409` | Another route has the same prefix. |

### Sample Request

```json
{
    "prefix": "+1",
    "trunks": ["019a5a3e-1b2c-7d4e-8f60-1a2b3c4d5e6f", "019a5a3e-5f6a-7b8c-9d0e-1f2a3b4c5d6e"]
}
```

### Sample Response

```json
{
    "result": {
        "id": "019a5a3f-2a3b-7c4d-8e5f-60718293a4b5",
        "prefix": "+1",
        "trunks": ["019a5a3e-1b2c-7d4e-8f60-1a2b3c4d5e6f", "019a5a3e-5f6a-7b8c-9d0e-1f2a3b4c5d6e"]
    }
}
```

## Update a SIP Route

This path replaces a SIP route. The request body is that of [Create a SIP Route](#create-a-sip-route).

| Method | Path                       |
| ------ | -------------------------- |
| PUT    | `/api/v1/sip-routes/{id}` |

### Path Parameters

| Name | Type   | Description |
| ---- | ------ | ----------- |
| `id` | string | Route identifier. |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `400` | A trunk does not exist. |
| `404` | No route has this identifier. |
| `409` | Another route has the same prefix. |

### Sample Request

```json
{
    "prefix": "+1",
    "trunks": ["019a5a3e-5f6a-7b8c-9d0e-1f2a3b4c5d6e", "019a5a3e-1b2c-7d4e-8f60-1a2b3c4d5e6f"]
}
```

### Sample Response

```json
{
    "result": {
        "id": "019a5a3f-2a3b-7c4d-8e5f-60718293a4b5",
        "prefix": "+1",
        "trunks": ["019a5a3e-5f6a-7b8c-9d0e-1f2a3b4c5d6e", "019a5a3e-1b2c-7d4e-8f60-1a2b3c4d5e6f"]
    }
}
```

## Delete a SIP Route

This path deletes a SIP route.

| Method | Path                       |
| ------ | -------------------------- |
| DELETE | `/api/v1/sip-routes/{id}` |

### Path Parameters

| Name | Type   | Description |
| ---- | ------ | ----------- |
| `id` | string | Route identifier. |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `404` | No route has this identifier. |

### Sample Response

```json
{
    "result": {
        "message": "SIP route deleted"
    }
}
```
