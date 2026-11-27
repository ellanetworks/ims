# SIP

The SIP names of Ella IMS derive from the operator settings.

## Get SIP Information

This path returns the home domain, the host names the IMS answers to, and the SIP listeners.

| Method | Path          |
| ------ | ------------- |
| GET    | `/api/v1/sip` |

### Response Fields

| Field                    | Type   | Description                                                                                                                                                                                               |
| ------------------------ | ------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `home_domain`            | string | Home network domain name: the operator `domain`, or the domain derived from the MCC and MNC if it is empty.                                                                                               |
| `aliases`                | array  | Host names the IMS answers to: the home domain, the domain derived from the MCC and MNC if it differs, the P-CSCF host name, and this node's S-CSCF host name, `scscf-<diameter label>.<derived domain>`. |
| `listeners`              | array  | SIP listeners.                                                                                                                                                                                            |
| `listeners[].role`       | string | `pcscf`, `pcscf-protected`, `icscf`, `scscf`, `ibcf`, `ibcf-external` or `ibcf-external-tls`.                                                                                                             |
| `listeners[].address`    | string | Address and port.                                                                                                                                                                                         |
| `listeners[].transports` | array  | Transports of the listener.                                                                                                                                                                               |

### Sample Response

```
{
    "result": {
        "home_domain": "ims.mnc001.mcc001.3gppnetwork.org",
        "aliases": [
            "ims.mnc001.mcc001.3gppnetwork.org",
            "pcscf.ims.mnc001.mcc001.3gppnetwork.org",
            "scscf-ims-a.ims.mnc001.mcc001.3gppnetwork.org"
        ],
        "listeners": [
            {
                "role": "pcscf",
                "address": "127.0.0.1:5060",
                "transports": ["udp", "tcp"]
            },
            {
                "role": "pcscf-protected",
                "address": "127.0.0.1:5063",
                "transports": ["udp", "tcp"]
            },
            {
                "role": "pcscf-protected",
                "address": "127.0.0.1:5064",
                "transports": ["udp", "tcp"]
            },
            {
                "role": "pcscf-protected",
                "address": "127.0.0.1:5065",
                "transports": ["udp", "tcp"]
            },
            {
                "role": "icscf",
                "address": "127.0.0.1:5070",
                "transports": ["udp", "tcp"]
            },
            {
                "role": "scscf",
                "address": "127.0.0.1:5080",
                "transports": ["udp", "tcp"]
            }
        ]
    }
}
```
