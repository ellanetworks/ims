# Operator

The operator is the PLMN of the IMS, from which its home domain and Diameter identity derive, and the numbering plan that converts dial strings to E.164 numbers.

## Get the Operator

This path returns the operator settings.

| Method | Path               |
| ------ | ------------------ |
| GET    | `/api/v1/operator` |

### Response Fields

| Field                            | Type   | Description                                  |
| -------------------------------- | ------ | -------------------------------------------- |
| `mcc`                            | string | Mobile Country Code.                         |
| `mnc`                            | string | Mobile Network Code.                         |
| `numbering`                      | object | Conversion of dial strings to E.164 numbers. |
| `numbering.country_code`         | string | Country code.                                |
| `numbering.national_prefix`      | string | National prefix.                             |
| `numbering.international_prefix` | string | International prefix.                        |

### Sample Response

```
{
    "result": {
        "mcc": "001",
        "mnc": "01",
        "numbering": {
            "country_code": "1",
            "national_prefix": "1",
            "international_prefix": "011"
        }
    }
}
```

## Update the Operator

This path replaces the operator settings. A change of MCC or MNC changes the home domain, and the IMS restarts Diameter and SIP under the new names.

| Method | Path               |
| ------ | ------------------ |
| PUT    | `/api/v1/operator` |

### Request Body

| Field                            | Type   | Required | Description                                                       |
| -------------------------------- | ------ | -------- | ----------------------------------------------------------------- |
| `mcc`                            | string | Yes      | 3 digits.                                                         |
| `mnc`                            | string | Yes      | 2 or 3 digits.                                                    |
| `numbering`                      | object | Yes      |                                                                   |
| `numbering.country_code`         | string | Yes      | 1 to 3 digits, not starting with 0.                               |
| `numbering.national_prefix`      | string | Yes      | Up to 4 digits. Differs from `international_prefix` unless empty. |
| `numbering.international_prefix` | string | Yes      | Up to 4 digits.                                                   |

### Sample Request

```
{
    "mcc": "001",
    "mnc": "01",
    "numbering": {
        "country_code": "1",
        "national_prefix": "1",
        "international_prefix": "011"
    }
}
```

### Sample Response

```
{
    "result": {
        "mcc": "001",
        "mnc": "01",
        "numbering": {
            "country_code": "1",
            "national_prefix": "1",
            "international_prefix": "011"
        }
    }
}
```
