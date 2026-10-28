# IP-SM-GW

The IP-SM-GW is Ella IMS's built-in IP Short Message Gateway: the Application Server that carries SMS over IP.

## Get the IP-SM-GW

This path returns the IP-SM-GW settings and name.

| Method | Path               |
| ------ | ------------------ |
| GET    | `/api/v1/ip-sm-gw` |

### Response Fields

| Field    | Type   | Description                                                                               |
| -------- | ------ | ----------------------------------------------------------------------------------------- |
| `number` | string | E.164 number of the IP-SM-GW, without `+`. Empty if not set.                              |
| `name`   | string | SIP URI of the IP-SM-GW, the server name of its iFCs. Derived from the operator settings. |

### Sample Response

```
{
    "result": {
        "number": "15550000100",
        "name": "sip:ipsmgw.ims.mnc001.mcc001.3gppnetwork.org"
    }
}
```

## Update the IP-SM-GW

This path sets the IP-SM-GW number. The IP-SM-GW registers the new number at the HSS for the users it serves. An empty number deregisters it.

| Method | Path               |
| ------ | ------------------ |
| PUT    | `/api/v1/ip-sm-gw` |

### Request Body

| Field    | Type   | Required | Description                                            |
| -------- | ------ | -------- | ------------------------------------------------------ |
| `number` | string | Yes      | E.164 number of 1 to 15 digits, without `+`, or empty. |

### Sample Request

```
{
    "number": "15550000100"
}
```

### Sample Response

```
{
    "result": {
        "number": "15550000100",
        "name": "sip:ipsmgw.ims.mnc001.mcc001.3gppnetwork.org"
    }
}
```
