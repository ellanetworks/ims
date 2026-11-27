# MCPTT Users

An MCPTT user (TS 24.484 §8.3) and the IMS public user identities its clients register at. The `/api/v1/mcptt` paths answer `404` when the IMS does not run the MCPTT AS.

## List Users

This path returns the list of users, in the order they were created in.

| Method | Path                  |
| ------ | --------------------- |
| GET    | `/api/v1/mcptt/users` |

### Query Parameters

| Name       | In    | Type | Default | Allowed | Description               |
| ---------- | ----- | ---- | ------- | ------- | ------------------------- |
| `page`     | query | int  | `1`     | `>= 1`  | 1-based page index.       |
| `per_page` | query | int  | `25`    | `1…100` | Number of items per page. |

### Response Fields

| Field                   | Type   | Description                                                                         |
| ----------------------- | ------ | ----------------------------------------------------------------------------------- |
| `mcptt_id`              | string | MCPTT ID of the user.                                                               |
| `alias`                 | string | Alias of the user (TS 24.484 §8.3.2.7). Empty if none.                              |
| `impus`                 | array  | IMS public user identities of the user, such as `sip:mcptt-<MSISDN>@<home domain>`. |
| `implicit_affiliations` | array  | Group IDs the user's clients are affiliated to when they register.                  |

### Sample Response

```
{
    "result": {
        "items": [
            {
                "mcptt_id": "sip:alice@mcptt.example.com",
                "alias": "Alice",
                "impus": ["sip:mcptt-15550000001@ims.mnc001.mcc001.3gppnetwork.org"],
                "implicit_affiliations": ["sip:fire@mcptt.example.com"]
            },
            {
                "mcptt_id": "sip:carol@mcptt.example.com",
                "alias": "Carol",
                "impus": ["sip:mcptt-15550000003@ims.mnc001.mcc001.3gppnetwork.org"],
                "implicit_affiliations": ["sip:police@mcptt.example.com"]
            },
            {
                "mcptt_id": "sip:bob@mcptt.example.com",
                "alias": "Bob",
                "impus": ["sip:mcptt-15550000002@ims.mnc001.mcc001.3gppnetwork.org"],
                "implicit_affiliations": []
            }
        ],
        "page": 1,
        "per_page": 25,
        "total_count": 3
    }
}
```

## Get a User

This path returns the details of a specific user.

| Method | Path                             |
| ------ | -------------------------------- |
| GET    | `/api/v1/mcptt/users/{mcptt_id}` |

### Path Parameters

| Name       | Type   | Description                                                      |
| ---------- | ------ | ---------------------------------------------------------------- |
| `mcptt_id` | string | MCPTT ID of the user, for example `sip:alice@mcptt.example.com`. |

### Response Codes

| Code  | Description                |
| ----- | -------------------------- |
| `404` | No user has this MCPTT ID. |

### Sample Response

```
{
    "result": {
        "mcptt_id": "sip:alice@mcptt.example.com",
        "alias": "Alice",
        "impus": ["sip:mcptt-15550000001@ims.mnc001.mcc001.3gppnetwork.org"],
        "implicit_affiliations": ["sip:fire@mcptt.example.com"]
    }
}
```

## Create a User

This path creates a user.

| Method | Path                  |
| ------ | --------------------- |
| POST   | `/api/v1/mcptt/users` |

### Request Body

| Field                   | Type   | Required | Description                                  |
| ----------------------- | ------ | -------- | -------------------------------------------- |
| `mcptt_id`              | string | Yes      | SIP URI. Unique among users and groups.      |
| `alias`                 | string | No       | Up to 64 characters.                         |
| `impus`                 | array  | Yes      | At least one.                                |
| `implicit_affiliations` | array  | No       | Group IDs of groups the user is a member of. |

### Response Codes

| Code  | Description                                                                           |
| ----- | ------------------------------------------------------------------------------------- |
| `409` | The request conflicts with other users or groups, such as an MCPTT ID already in use. |

### Sample Request

```
{
    "mcptt_id": "sip:alice@mcptt.example.com",
    "alias": "Alice",
    "impus": ["sip:mcptt-15550000001@ims.mnc001.mcc001.3gppnetwork.org"]
}
```

### Sample Response

```
{
    "result": {
        "mcptt_id": "sip:alice@mcptt.example.com",
        "alias": "Alice",
        "impus": ["sip:mcptt-15550000001@ims.mnc001.mcc001.3gppnetwork.org"],
        "implicit_affiliations": []
    }
}
```

## Update a User

This path replaces a user. The request body is that of [Create a User](#create-a-user); `mcptt_id` is optional and, if present, must be that of the path.

| Method | Path                             |
| ------ | -------------------------------- |
| PUT    | `/api/v1/mcptt/users/{mcptt_id}` |

### Path Parameters

| Name       | Type   | Description           |
| ---------- | ------ | --------------------- |
| `mcptt_id` | string | MCPTT ID of the user. |

### Response Codes

| Code  | Description                                                                                                               |
| ----- | ------------------------------------------------------------------------------------------------------------------------- |
| `404` | No user has this MCPTT ID.                                                                                                |
| `409` | The request conflicts with other users or groups, such as an implicit affiliation to a group the user is not a member of. |

### Sample Request

```
{
    "alias": "Carol",
    "impus": ["sip:mcptt-15550000003@ims.mnc001.mcc001.3gppnetwork.org"],
    "implicit_affiliations": ["sip:police@mcptt.example.com"]
}
```

### Sample Response

```
{
    "result": {
        "mcptt_id": "sip:carol@mcptt.example.com",
        "alias": "Carol",
        "impus": ["sip:mcptt-15550000003@ims.mnc001.mcc001.3gppnetwork.org"],
        "implicit_affiliations": ["sip:police@mcptt.example.com"]
    }
}
```

## Delete a User

This path deletes a user. The user leaves its groups, and its clients are logged off and leave the group calls they are in.

| Method | Path                             |
| ------ | -------------------------------- |
| DELETE | `/api/v1/mcptt/users/{mcptt_id}` |

### Path Parameters

| Name       | Type   | Description           |
| ---------- | ------ | --------------------- |
| `mcptt_id` | string | MCPTT ID of the user. |

### Response Codes

| Code  | Description                                          |
| ----- | ---------------------------------------------------- |
| `404` | No user has this MCPTT ID.                           |
| `409` | A group the user leaves would be invalid without it. |

### Sample Response

```
{
    "result": {
        "message": "user deleted"
    }
}
```
