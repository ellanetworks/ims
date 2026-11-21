# Operator

The operator is the PLMN of the IMS, from which its Diameter identity derives, its home network domain, and the numbering plan that converts dial strings to E.164 numbers.

## Get the Operator

This path returns the operator settings.

| Method | Path               |
| ------ | ------------------ |
| GET    | `/api/v1/operator` |

### Response Fields

| Field                            | Type    | Description                                                                                                |
| -------------------------------- | ------- | ---------------------------------------------------------------------------------------------------------- |
| `mcc`                            | string  | Mobile Country Code.                                                                                       |
| `mnc`                            | string  | Mobile Network Code.                                                                                       |
| `domain`                         | string  | Home network domain name. Empty: `ims.mnc<MNC>.mcc<MCC>.3gppnetwork.org`, with the MNC padded to 3 digits. |
| `numbering`                      | object  | Conversion of dial strings to E.164 numbers.                                                               |
| `numbering.country_code`         | string  | Country code.                                                                                              |
| `numbering.national_prefix`      | string  | National prefix.                                                                                           |
| `numbering.international_prefix` | string  | International prefix.                                                                                      |
| `numbering.min_national_digits`  | integer | Fewest digits a number dialled without a prefix needs to be routed to a SIP trunk. `0`: never.             |

### Sample Response

```
{
    "result": {
        "mcc": "001",
        "mnc": "01",
        "domain": "",
        "numbering": {
            "country_code": "1",
            "national_prefix": "1",
            "international_prefix": "011",
            "min_national_digits": 7
        }
    }
}
```

## Update the Operator

This path replaces the operator settings. A change of MCC, MNC or domain restarts Diameter and SIP under the new names.

The domain derived from the MCC and MNC stays a home domain when `domain` is set.

| Method | Path               |
| ------ | ------------------ |
| PUT    | `/api/v1/operator` |

### Request Body

| Field                            | Type    | Required | Description                                                                                                                                                                                        |
| -------------------------------- | ------- | -------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `mcc`                            | string  | Yes      | 3 digits.                                                                                                                                                                                          |
| `mnc`                            | string  | Yes      | 2 or 3 digits.                                                                                                                                                                                     |
| `domain`                         | string  | No       | Domain name of at least two labels, up to 253 characters, stored in lowercase. Under `3gppnetwork.org`, only the domain derived from the MCC and MNC. Default empty: derived from the MCC and MNC. |
| `numbering`                      | object  | Yes      |                                                                                                                                                                                                    |
| `numbering.country_code`         | string  | Yes      | 1 to 3 digits, not starting with 0.                                                                                                                                                                |
| `numbering.national_prefix`      | string  | Yes      | Up to 4 digits. Differs from `international_prefix` unless empty.                                                                                                                                  |
| `numbering.international_prefix` | string  | Yes      | Up to 4 digits.                                                                                                                                                                                    |
| `numbering.min_national_digits`  | integer | No       | 0 to 15. Default `7`. Fewest digits a number dialled without a prefix needs to be routed to a SIP trunk. `0`: never.                                                                               |

### Sample Request

```
{
    "mcc": "001",
    "mnc": "01",
    "domain": "",
    "numbering": {
        "country_code": "1",
        "national_prefix": "1",
        "international_prefix": "011",
        "min_national_digits": 7
    }
}
```

### Sample Response

```
{
    "result": {
        "mcc": "001",
        "mnc": "01",
        "domain": "",
        "numbering": {
            "country_code": "1",
            "national_prefix": "1",
            "international_prefix": "011",
            "min_national_digits": 7
        }
    }
}
```
