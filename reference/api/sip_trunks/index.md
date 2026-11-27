# SIP Trunks

SIP trunks connect Ella IMS to carriers. SIP routes pick the trunks of a call by the prefix of the number. See [SIP Trunks](https://docs.ellanetworks.com/ims/explanation/sip_trunks/index.md).

## List SIP Trunks

This path returns the list of SIP trunks, ordered by identifier.

| Method | Path                 |
| ------ | -------------------- |
| GET    | `/api/v1/sip-trunks` |

### Response Fields

| Field                        | Type    | Description                                                                                                                                                                                          |
| ---------------------------- | ------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `id`                         | string  | Trunk identifier, a UUIDv7.                                                                                                                                                                          |
| `name`                       | string  | Name of the trunk.                                                                                                                                                                                   |
| `addresses`                  | array   | Where requests to the carrier go, in order.                                                                                                                                                          |
| `addresses[].host`           | string  | Domain name, or IPv4 or IPv6 address.                                                                                                                                                                |
| `addresses[].port`           | int     | Port. Absent if not set.                                                                                                                                                                             |
| `addresses[].transport`      | string  | `udp`, `tcp` or `tls`. Absent if not set.                                                                                                                                                            |
| `inbound_cidrs`              | array   | Networks that requests from the carrier come from. Empty if not set.                                                                                                                                 |
| `domain`                     | string  | Host of the Request-URI of requests to the carrier. Empty if not set.                                                                                                                                |
| `trusted`                    | boolean | Whether the carrier is in the trust domain.                                                                                                                                                          |
| `keepalive_interval`         | int     | Seconds between keepalives to the carrier. `0`: no OPTIONS requests.                                                                                                                                 |
| `enabled`                    | boolean | Whether the trunk carries calls, in both directions.                                                                                                                                                 |
| `tls`                        | object  | TLS with the carrier. See [TLS](#tls).                                                                                                                                                               |
| `tls.required`               | boolean | Whether the carrier must use TLS, with a certificate.                                                                                                                                                |
| `tls.ca_certificates`        | string  | PEM-encoded CA certificates. Empty if none.                                                                                                                                                          |
| `tls.pins`                   | array   | SHA-256 fingerprints of pinned certificates, as upper-case hex bytes separated by colons.                                                                                                            |
| `tls.identities`             | array   | Domain names one of which the carrier's certificate must hold. Empty if not set.                                                                                                                     |
| `tls.ca_certificate_details` | array   | One entry per CA certificate, in order. See [Certificate Details](#certificate-details).                                                                                                             |
| `effective.domain`           | string  | `domain` in use, default included.                                                                                                                                                                   |
| `effective.inbound_cidrs`    | array   | `inbound_cidrs` in use, default included.                                                                                                                                                            |
| `effective.tls_identities`   | array   | `tls.identities` in use, default included.                                                                                                                                                           |
| `status.state`               | string  | `up`: the carrier answers OPTIONS requests. `down`: it does not; routes skip the trunk. `unknown`: not probed. `unusable`: conflicts with the configuration file or another trunk; carries no calls. |
| `status.since`               | string  | Time of the last state change (RFC 3339). Absent while `unknown`.                                                                                                                                    |

### Certificate Details

| Field                | Type    | Description                                                       |
| -------------------- | ------- | ----------------------------------------------------------------- |
| `subject`            | string  | Subject distinguished name.                                       |
| `issuer`             | string  | Issuer distinguished name.                                        |
| `dns_names`          | array   | DNS names of the subjectAltName.                                  |
| `uris`               | array   | URIs of the subjectAltName.                                       |
| `ip_addresses`       | array   | IP addresses of the subjectAltName.                               |
| `identities`         | array   | Identities matched against `tls.identities`.                      |
| `ca`                 | boolean | Whether the certificate is a CA certificate.                      |
| `not_before`         | string  | Start of validity (RFC 3339).                                     |
| `not_after`          | string  | End of validity (RFC 3339).                                       |
| `sha256_fingerprint` | string  | SHA-256 fingerprint, as upper-case hex bytes separated by colons. |

### Sample Response

```
{
    "result": {
        "items": [
            {
                "id": "019a5a3e-1b2c-7d4e-8f60-1a2b3c4d5e6f",
                "name": "Carrier",
                "addresses": [
                    {
                        "host": "sip.carrier.example",
                        "transport": "tcp"
                    }
                ],
                "inbound_cidrs": ["198.51.100.10/32", "198.51.100.11/32"],
                "domain": "",
                "trusted": true,
                "keepalive_interval": 30,
                "enabled": true,
                "tls": {
                    "required": false,
                    "ca_certificates": "",
                    "pins": [],
                    "identities": [],
                    "ca_certificate_details": []
                },
                "effective": {
                    "domain": "sip.carrier.example",
                    "inbound_cidrs": ["198.51.100.10/32", "198.51.100.11/32"],
                    "tls_identities": []
                },
                "status": {
                    "state": "up",
                    "since": "2026-11-06T10:00:00.000Z"
                }
            }
        ]
    }
}
```

## Get a SIP Trunk

This path returns the details of a specific SIP trunk.

| Method | Path                      |
| ------ | ------------------------- |
| GET    | `/api/v1/sip-trunks/{id}` |

### Path Parameters

| Name | Type   | Description       |
| ---- | ------ | ----------------- |
| `id` | string | Trunk identifier. |

### Response Codes

| Code  | Description                   |
| ----- | ----------------------------- |
| `404` | No trunk has this identifier. |

### Sample Response

```
{
    "result": {
        "id": "019a5a3e-1b2c-7d4e-8f60-1a2b3c4d5e6f",
        "name": "Carrier",
        "addresses": [
            {
                "host": "sip.carrier.example",
                "transport": "tcp"
            }
        ],
        "inbound_cidrs": ["198.51.100.10/32", "198.51.100.11/32"],
        "domain": "",
        "trusted": true,
        "keepalive_interval": 30,
        "enabled": true,
        "tls": {
            "required": false,
            "ca_certificates": "",
            "pins": [],
            "identities": [],
            "ca_certificate_details": []
        },
        "effective": {
            "domain": "sip.carrier.example",
            "inbound_cidrs": ["198.51.100.10/32", "198.51.100.11/32"],
            "tls_identities": []
        },
        "status": {
            "state": "up",
            "since": "2026-11-06T10:00:00.000Z"
        }
    }
}
```

## Create a SIP Trunk

This path creates a SIP trunk.

| Method | Path                 |
| ------ | -------------------- |
| POST   | `/api/v1/sip-trunks` |

### Request Body

| Field                   | Type    | Required | Description                                                                                                                                                                                                                                                                                                                                                  |
| ----------------------- | ------- | -------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `name`                  | string  | Yes      | 1 to 64 printable characters, without leading or trailing spaces. Unique among trunks, ignoring case.                                                                                                                                                                                                                                                        |
| `addresses`             | array   | Yes      | At least one, each once, none reaching the same server as an address of another trunk: the same host, with a port and a transport that can match once their defaults apply. See [Addresses and DNS](#addresses-and-dns).                                                                                                                                     |
| `addresses[].host`      | string  | Yes      | Domain name, or specific IPv4 or IPv6 address.                                                                                                                                                                                                                                                                                                               |
| `addresses[].port`      | int     | No       | 1–65535.                                                                                                                                                                                                                                                                                                                                                     |
| `addresses[].transport` | string  | No       | `udp`, `tcp` or `tls`. `tls` needs `tls.ca_certificates` or `tls.pins`.                                                                                                                                                                                                                                                                                      |
| `inbound_cidrs`         | array   | No       | Networks, such as `198.51.100.0/24`, or single addresses. No two may overlap, in this trunk or across trunks. None may overlap `sip.addresses` or `sip.trusted_networks` of the [configuration file](https://docs.ellanetworks.com/ims/reference/config_file/index.md). Not `0.0.0.0/0` or `::/0`. Default: the IP addresses among the hosts of `addresses`. |
| `domain`                | string  | No       | Domain name, or specific IPv4 or IPv6 address, without brackets. Default: the first domain name among the hosts of `addresses`. Required if every host is an IP address.                                                                                                                                                                                     |
| `trusted`               | boolean | No       | Default `false`. A trusted trunk never uses UDP: no address may have `udp` as transport.                                                                                                                                                                                                                                                                     |
| `keepalive_interval`    | int     | No       | 5–3600 seconds, or `0` for no OPTIONS requests. Default `30`.                                                                                                                                                                                                                                                                                                |
| `enabled`               | boolean | No       | Default `true`.                                                                                                                                                                                                                                                                                                                                              |
| `tls`                   | object  | No       | Absent for a trunk without TLS. See [TLS](#tls).                                                                                                                                                                                                                                                                                                             |
| `tls.required`          | boolean | No       | Default `false`. Every address must have `tls` as transport. Requires `ibcf.external.tls` in the [configuration file](https://docs.ellanetworks.com/ims/reference/config_file/index.md).                                                                                                                                                                     |
| `tls.ca_certificates`   | string  | No       | PEM-encoded CA certificates, at most 32.                                                                                                                                                                                                                                                                                                                     |
| `tls.pins`              | array   | No       | At most 32, each a PEM-encoded certificate, or a SHA-256 fingerprint as 32 hex bytes, with or without colons.                                                                                                                                                                                                                                                |
| `tls.identities`        | array   | No       | Domain names, at most 16. Default: `domain` and the domain names among the hosts of `addresses`. Required with `tls.ca_certificates` if there are none.                                                                                                                                                                                                      |

### Addresses and DNS

| `host`      | `transport` | `port`   | Requests go to                                                                       |
| ----------- | ----------- | -------- | ------------------------------------------------------------------------------------ |
| Domain name | —           | —        | The NAPTR, then SRV, then A and AAAA records of the domain.                          |
| Domain name | Set         | —        | The SRV records for that transport, else the A and AAAA records at the default port. |
| Domain name | Set or —    | Set      | The A and AAAA records, at that port, over that transport, else UDP.                 |
| IP address  | Set or —    | Set or — | That address, at that port, else the default port, over that transport, else UDP.    |

The default port is `5060` for `udp` and `tcp`, and `5061` for `tls`. A trusted trunk skips UDP records, and uses TCP instead of UDP.

### TLS

| Carrier authenticated by | Field                 | Carrier's certificate must hold                              |
| ------------------------ | --------------------- | ------------------------------------------------------------ |
| A CA                     | `tls.ca_certificates` | One of `tls.identities`                                      |
| Its own certificate      | `tls.pins`            | One of `tls.identities`, unless the address is an IP address |

An identity is the host of a `sip` URI or a DNS name in the certificate's subjectAltName, or its common name if it has none.

| TLS connection opened by | Extended key usage of each certificate of the carrier's chain, if present |
| ------------------------ | ------------------------------------------------------------------------- |
| Ella IMS                 | `serverAuth`, `anyExtendedKeyUsage` or `id-kp-sipDomain`                  |
| The carrier              | `clientAuth`, `anyExtendedKeyUsage` or `id-kp-sipDomain`                  |

With `tls.required`, the carrier must use TLS, and present a certificate on the TLS connections it opens.

CA certificates and PEM pins must not be expired when added. Ella IMS keeps the fingerprint of a PEM pin only.

Ella IMS's own certificate is `ibcf.external.tls` in the [configuration file](https://docs.ellanetworks.com/ims/reference/config_file/index.md).

### Response Codes

| Code  | Description                                                                                                                                               |
| ----- | --------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `400` | A field is invalid, an inbound network overlaps the networks of the configuration file, or `tls.required` is set without `ibcf.external.tls`.             |
| `409` | Another trunk has the same name, an address that reaches the same server as one of this trunk's, or an inbound network that overlaps one of this trunk's. |

### Sample Request

```
{
    "name": "Carrier",
    "addresses": [
        {
            "host": "sip.carrier.example",
            "transport": "tcp"
        }
    ],
    "inbound_cidrs": ["198.51.100.10/32", "198.51.100.11/32"],
    "trusted": true
}
```

### Sample Response

```
{
    "result": {
        "id": "019a5a3e-1b2c-7d4e-8f60-1a2b3c4d5e6f",
        "name": "Carrier",
        "addresses": [
            {
                "host": "sip.carrier.example",
                "transport": "tcp"
            }
        ],
        "inbound_cidrs": ["198.51.100.10/32", "198.51.100.11/32"],
        "domain": "",
        "trusted": true,
        "keepalive_interval": 30,
        "enabled": true,
        "tls": {
            "required": false,
            "ca_certificates": "",
            "pins": [],
            "identities": [],
            "ca_certificate_details": []
        },
        "effective": {
            "domain": "sip.carrier.example",
            "inbound_cidrs": ["198.51.100.10/32", "198.51.100.11/32"],
            "tls_identities": []
        },
        "status": {
            "state": "unknown"
        }
    }
}
```

### Sample Request with TLS

```
{
    "name": "Carrier",
    "addresses": [
        {
            "host": "sip.carrier.example",
            "transport": "tls"
        }
    ],
    "inbound_cidrs": ["198.51.100.10/32", "198.51.100.11/32"],
    "trusted": true,
    "tls": {
        "required": true,
        "ca_certificates": "-----BEGIN CERTIFICATE-----\nMIIBczCCARigAwIBAgII...\n-----END CERTIFICATE-----\n"
    }
}
```

The `tls` object of its response:

```
{
    "required": true,
    "ca_certificates": "-----BEGIN CERTIFICATE-----\nMIIBczCCARigAwIBAgII...\n-----END CERTIFICATE-----\n",
    "pins": [],
    "identities": [],
    "ca_certificate_details": [
        {
            "subject": "CN=Carrier CA,O=Carrier",
            "issuer": "CN=Carrier CA,O=Carrier",
            "dns_names": [],
            "uris": [],
            "ip_addresses": [],
            "identities": [],
            "ca": true,
            "not_before": "2026-01-01T00:00:00.000Z",
            "not_after": "2036-01-01T00:00:00.000Z",
            "sha256_fingerprint": "2C:54:DF:66:29:19:96:C5:C8:A5:E8:4B:7A:32:FE:71:02:38:11:62:D4:D9:2E:2C:A1:C4:E7:8C:0D:D5:B1:C5"
        }
    ]
}
```

## Update a SIP Trunk

This path replaces a SIP trunk. The request body is that of [Create a SIP Trunk](#create-a-sip-trunk). A field left out takes its default. A response is a valid request body without `id`, `effective`, `status` and `tls.ca_certificate_details`.

| Method | Path                      |
| ------ | ------------------------- |
| PUT    | `/api/v1/sip-trunks/{id}` |

### Path Parameters

| Name | Type   | Description       |
| ---- | ------ | ----------------- |
| `id` | string | Trunk identifier. |

### Response Codes

| Code  | Description                                                                                                                                               |
| ----- | --------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `400` | A field is invalid, an inbound network overlaps the networks of the configuration file, or `tls.required` is set without `ibcf.external.tls`.             |
| `404` | No trunk has this identifier.                                                                                                                             |
| `409` | Another trunk has the same name, an address that reaches the same server as one of this trunk's, or an inbound network that overlaps one of this trunk's. |

### Sample Request

```
{
    "name": "Carrier",
    "addresses": [
        {
            "host": "sip.carrier.example",
            "transport": "tcp"
        }
    ],
    "inbound_cidrs": ["198.51.100.10/32", "198.51.100.11/32"],
    "trusted": true,
    "enabled": false
}
```

### Sample Response

```
{
    "result": {
        "id": "019a5a3e-1b2c-7d4e-8f60-1a2b3c4d5e6f",
        "name": "Carrier",
        "addresses": [
            {
                "host": "sip.carrier.example",
                "transport": "tcp"
            }
        ],
        "inbound_cidrs": ["198.51.100.10/32", "198.51.100.11/32"],
        "domain": "",
        "trusted": true,
        "keepalive_interval": 30,
        "enabled": false,
        "tls": {
            "required": false,
            "ca_certificates": "",
            "pins": [],
            "identities": [],
            "ca_certificate_details": []
        },
        "effective": {
            "domain": "sip.carrier.example",
            "inbound_cidrs": ["198.51.100.10/32", "198.51.100.11/32"],
            "tls_identities": []
        },
        "status": {
            "state": "unknown"
        }
    }
}
```

## Delete a SIP Trunk

This path deletes a SIP trunk that no route uses.

| Method | Path                      |
| ------ | ------------------------- |
| DELETE | `/api/v1/sip-trunks/{id}` |

### Path Parameters

| Name | Type   | Description       |
| ---- | ------ | ----------------- |
| `id` | string | Trunk identifier. |

### Response Codes

| Code  | Description                   |
| ----- | ----------------------------- |
| `404` | No trunk has this identifier. |
| `409` | A route uses the trunk.       |

### Sample Response

```
{
    "result": {
        "message": "SIP trunk deleted"
    }
}
```

## List SIP Routes

This path returns the list of SIP routes, ordered by identifier.

| Method | Path                 |
| ------ | -------------------- |
| GET    | `/api/v1/sip-routes` |

### Response Fields

| Field    | Type   | Description                                                                                                          |
| -------- | ------ | -------------------------------------------------------------------------------------------------------------------- |
| `id`     | string | Route identifier, a UUIDv7.                                                                                          |
| `prefix` | string | Start of the E.164 numbers the route takes, with `+`. The longest matching prefix wins. Empty for the default route. |
| `trunks` | array  | Identifiers of the trunks of the route, in the order calls try them.                                                 |

### Sample Response

```
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

| Method | Path                      |
| ------ | ------------------------- |
| GET    | `/api/v1/sip-routes/{id}` |

### Path Parameters

| Name | Type   | Description       |
| ---- | ------ | ----------------- |
| `id` | string | Route identifier. |

### Response Codes

| Code  | Description                   |
| ----- | ----------------------------- |
| `404` | No route has this identifier. |

### Sample Response

```
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

| Method | Path                 |
| ------ | -------------------- |
| POST   | `/api/v1/sip-routes` |

### Request Body

| Field    | Type   | Required | Description                                                                                                        |
| -------- | ------ | -------- | ------------------------------------------------------------------------------------------------------------------ |
| `prefix` | string | No       | `+` followed by 1 to 15 digits, not starting with `0`. Absent or empty for the default route. Unique among routes. |
| `trunks` | array  | Yes      | Identifiers of existing trunks, at least one, each once.                                                           |

### Response Codes

| Code  | Description                                       |
| ----- | ------------------------------------------------- |
| `400` | The prefix is invalid, or a trunk does not exist. |
| `409` | Another route has the same prefix.                |

### Sample Request

```
{
    "prefix": "+1",
    "trunks": ["019a5a3e-1b2c-7d4e-8f60-1a2b3c4d5e6f", "019a5a3e-5f6a-7b8c-9d0e-1f2a3b4c5d6e"]
}
```

### Sample Response

```
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

| Method | Path                      |
| ------ | ------------------------- |
| PUT    | `/api/v1/sip-routes/{id}` |

### Path Parameters

| Name | Type   | Description       |
| ---- | ------ | ----------------- |
| `id` | string | Route identifier. |

### Response Codes

| Code  | Description                                       |
| ----- | ------------------------------------------------- |
| `400` | The prefix is invalid, or a trunk does not exist. |
| `404` | No route has this identifier.                     |
| `409` | Another route has the same prefix.                |

### Sample Request

```
{
    "prefix": "+1",
    "trunks": ["019a5a3e-5f6a-7b8c-9d0e-1f2a3b4c5d6e", "019a5a3e-1b2c-7d4e-8f60-1a2b3c4d5e6f"]
}
```

### Sample Response

```
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

| Method | Path                      |
| ------ | ------------------------- |
| DELETE | `/api/v1/sip-routes/{id}` |

### Path Parameters

| Name | Type   | Description       |
| ---- | ------ | ----------------- |
| `id` | string | Route identifier. |

### Response Codes

| Code  | Description                   |
| ----- | ----------------------------- |
| `404` | No route has this identifier. |

### Sample Response

```
{
    "result": {
        "message": "SIP route deleted"
    }
}
```
