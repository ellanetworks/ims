# Registrations

A registration is a private user identity registered with the S-CSCF, with its registered implicit registration sets.

## List Registrations

This path returns the list of registered private identities.

| Method | Path                    |
| ------ | ----------------------- |
| GET    | `/api/v1/registrations` |

### Query Parameters

| Name       | In    | Type | Default | Allowed | Description                                |
| ---------- | ----- | ---- | ------- | ------- | ------------------------------------------ |
| `search`   | query | str  |         |         | Substring of a private or public identity. |
| `page`     | query | int  | `1`     | `>= 1`  | 1-based page index.                        |
| `per_page` | query | int  | `25`    | `1…100` | Number of items per page.                  |

### Response Fields

| Field                                     | Type    | Description                                                                                                                  |
| ----------------------------------------- | ------- | ---------------------------------------------------------------------------------------------------------------------------- |
| `impi`                                    | string  | Private user identity.                                                                                                       |
| `implicit_registration_sets`              | array   | Registered implicit registration sets of the private identity. A set registers and deregisters as one (TS 23.228 §5.2.1a.0). |
| `implicit_registration_sets[].hss`        | object  | Origin-Host (`host`) and Origin-Realm (`realm`) of the last Cx answer for the set. Absent while unknown.                     |
| `implicit_registration_sets[].identities` | array   | Public user identities of the set.                                                                                           |
| `identities[].uri`                        | string  | Public user identity.                                                                                                        |
| `identities[].display_name`               | string  | Display name. Absent if none.                                                                                                |
| `identities[].barred`                     | boolean | Whether the identity is barred.                                                                                              |
| `identities[].registered_with`            | array   | Other private identities a request to this public identity reaches (TS 24.229 §5.4.3.3 step 8).                              |
| `implicit_registration_sets[].contacts`   | array   | Unexpired contacts bound to the set. A contact bound to several sets is listed in each.                                      |
| `contacts[].contact`                      | string  | Contact URI.                                                                                                                 |
| `contacts[].instance`                     | string  | Instance ID, an IMEI URN when available (TS 23.003 §13.8). Absent if none.                                                   |
| `contacts[].reg_id`                       | int     | reg-id of the flow (RFC 5626). Absent if none.                                                                               |
| `contacts[].q`                            | number  | q-value (RFC 3841 §7.2.3).                                                                                                   |
| `contacts[].media`                        | array   | Media the device registered for: `audio`, `video`.                                                                           |
| `contacts[].registered_at`                | string  | Registration time (RFC 3339).                                                                                                |
| `contacts[].expires_at`                   | string  | Expiry time (RFC 3339).                                                                                                      |
| `contacts[].address`                      | string  | Address and port of the device, as seen by the P-CSCF. Absent if unknown.                                                    |
| `contacts[].transport`                    | string  | `udp` or `tcp`. Absent if unknown.                                                                                           |
| `contacts[].protected`                    | boolean | Whether IPsec protects the signalling of the device.                                                                         |
| `contacts[].signalling_path`              | string  | State of the IMS signalling path of the device: `unmonitored`, `monitored` or `lost`.                                        |

### Sample Response

```
{
    "result": {
        "items": [
            {
                "impi": "001010000000001@ims.mnc001.mcc001.3gppnetwork.org",
                "implicit_registration_sets": [
                    {
                        "hss": {
                            "host": "hss.epc.mnc001.mcc001.3gppnetwork.org",
                            "realm": "epc.mnc001.mcc001.3gppnetwork.org"
                        },
                        "identities": [
                            {
                                "uri": "sip:001010000000001@ims.mnc001.mcc001.3gppnetwork.org",
                                "barred": true,
                                "registered_with": []
                            },
                            {
                                "uri": "tel:+15550000001",
                                "barred": false,
                                "registered_with": []
                            }
                        ],
                        "contacts": [
                            {
                                "contact": "sip:001010000000001@10.45.0.2:5064",
                                "instance": "urn:gsma:imei:35000000-000001-0",
                                "reg_id": 1,
                                "q": 1,
                                "media": ["audio", "video"],
                                "registered_at": "2026-10-26T18:20:04.512Z",
                                "expires_at": "2026-10-26T19:20:04.512Z",
                                "address": "10.45.0.2:5064",
                                "transport": "udp",
                                "protected": true,
                                "signalling_path": "monitored"
                            }
                        ]
                    }
                ]
            }
        ],
        "page": 1,
        "per_page": 25,
        "total_count": 1
    }
}
```

## Re-authenticate a Registration

This path brings forward the expiry of the bindings of a registered private identity and has its next REGISTER challenged (TS 24.229 §5.4.1.6).

| Method | Path                                          |
| ------ | --------------------------------------------- |
| POST   | `/api/v1/registrations/{impi}/reauthenticate` |

### Path Parameters

| Name   | Type   | Description            |
| ------ | ------ | ---------------------- |
| `impi` | string | Private user identity. |

### Response Codes

| Code  | Description                                  |
| ----- | -------------------------------------------- |
| `202` | Re-authentication started.                   |
| `404` | The private identity is not registered.      |
| `503` | The IMS is restarting to apply its settings. |

### Sample Response

```
{
    "result": {
        "impi": "001010000000001@ims.mnc001.mcc001.3gppnetwork.org"
    }
}
```
