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

## List SMS Routes

This path returns the SMS routes, ordered by identifier. An SMS route sends the short messages from phones of a service centre address to the Diameter peers of its SMS-IWMSC.

| Method | Path                      |
| ------ | ------------------------- |
| GET    | `/api/v1/ip-sm-gw/routes` |

### Response Fields

| Field            | Type   | Description                                                                                                                          |
| ---------------- | ------ | ------------------------------------------------------------------------------------------------------------------------------------ |
| `id`             | string | Route identifier, a UUIDv7.                                                                                                          |
| `service_centre` | string | E.164 service centre address, without `+`. Empty for the default route, which serves the service centre addresses of no other route. |
| `peers`          | array  | Identifiers of the Diameter peers of the route. Short messages try them by peer priority, lowest first.                              |

### Sample Response

```
{
    "result": {
        "items": [
            {
                "id": "01a1796b-90b3-74bc-b148-bdc28999206e",
                "service_centre": "15550000000",
                "peers": ["01a1796b-90b3-74bc-b148-bdc28999206a"]
            }
        ]
    }
}
```

## Get an SMS Route

| Method | Path                           |
| ------ | ------------------------------ |
| GET    | `/api/v1/ip-sm-gw/routes/{id}` |

The response has the fields of [List SMS Routes](#list-sms-routes).

## Create an SMS Route

| Method | Path                      |
| ------ | ------------------------- |
| POST   | `/api/v1/ip-sm-gw/routes` |

### Request Body

| Field            | Type   | Required | Description                                                                                                      |
| ---------------- | ------ | -------- | ---------------------------------------------------------------------------------------------------------------- |
| `service_centre` | string | No       | E.164 number of 1 to 15 digits, without `+`, or empty for the default route. Unique among routes. Default empty. |
| `peers`          | array  | Yes      | At least one Diameter peer identifier, each once. Each peer serves `sgd` and has a host.                         |

### Response Codes

| Code  | Description                                                                               |
| ----- | ----------------------------------------------------------------------------------------- |
| `409` | Another route has the same service centre, or a peer does not serve `sgd` or has no host. |

### Sample Request

```
{
    "service_centre": "15550000000",
    "peers": ["01a1796b-90b3-74bc-b148-bdc28999206a"]
}
```

## Update an SMS Route

| Method | Path                           |
| ------ | ------------------------------ |
| PUT    | `/api/v1/ip-sm-gw/routes/{id}` |

The request body and response codes are those of [Create an SMS Route](#create-an-sms-route).

## Delete an SMS Route

| Method | Path                           |
| ------ | ------------------------------ |
| DELETE | `/api/v1/ip-sm-gw/routes/{id}` |
