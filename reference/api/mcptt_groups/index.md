# MCPTT Groups

An MCPTT group is a prearranged group (TS 24.481 §7.2) whose calls use AMR-WB. The `/api/v1/mcptt` paths answer `404` when the IMS does not run the MCPTT AS.

## List Groups

This path returns the list of groups, in the order they were created in.

| Method | Path                   |
| ------ | ---------------------- |
| GET    | `/api/v1/mcptt/groups` |

### Query Parameters

| Name       | In    | Type | Default | Allowed | Description               |
| ---------- | ----- | ---- | ------- | ------- | ------------------------- |
| `page`     | query | int  | `1`     | `>= 1`  | 1-based page index.       |
| `per_page` | query | int  | `25`    | `1…100` | Number of items per page. |

### Response Fields

| Field                            | Type    | Description                                                                                                                                                                   |
| -------------------------------- | ------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `group_id`                       | string  | Group ID.                                                                                                                                                                     |
| `display_name`                   | string  | Group name (TS 24.481 §7.2.2). Empty if none.                                                                                                                                 |
| `hang_timer_ms`                  | int     | How long a call lasts without anyone talking, T4 (TS 24.380 Table 11.1.3-1). `0` releases the call as soon as the floor is idle. Absent if not set, in which case it is 30 s. |
| `maximum_duration_ms`            | int     | How long a call lasts at most, TNG3 (TS 24.379 §6.3.3.5).                                                                                                                     |
| `release_on_initiator_leave`     | boolean | Whether a call ends when the member that started it leaves (TS 24.379 §6.3.8.1).                                                                                              |
| `members`                        | array   | Members of the group.                                                                                                                                                         |
| `members[].mcptt_id`             | string  | MCPTT ID of the member.                                                                                                                                                       |
| `members[].recvonly`             | boolean | Whether the member may listen but not talk.                                                                                                                                   |
| `members[].implicit_affiliation` | boolean | Whether the group is one of the member's `implicit_affiliations`.                                                                                                             |

### Sample Response

```
{
    "result": {
        "items": [
            {
                "group_id": "sip:fire@mcptt.example.com",
                "display_name": "Fire",
                "maximum_duration_ms": 300000,
                "release_on_initiator_leave": false,
                "members": [
                    {
                        "mcptt_id": "sip:alice@mcptt.example.com",
                        "recvonly": false,
                        "implicit_affiliation": true
                    }
                ]
            },
            {
                "group_id": "sip:police@mcptt.example.com",
                "display_name": "Police",
                "hang_timer_ms": 10000,
                "maximum_duration_ms": 600000,
                "release_on_initiator_leave": true,
                "members": [
                    {
                        "mcptt_id": "sip:bob@mcptt.example.com",
                        "recvonly": false,
                        "implicit_affiliation": false
                    },
                    {
                        "mcptt_id": "sip:carol@mcptt.example.com",
                        "recvonly": false,
                        "implicit_affiliation": true
                    }
                ]
            }
        ],
        "page": 1,
        "per_page": 25,
        "total_count": 2
    }
}
```

## Get a Group

This path returns the details of a specific group.

| Method | Path                              |
| ------ | --------------------------------- |
| GET    | `/api/v1/mcptt/groups/{group_id}` |

### Path Parameters

| Name       | Type   | Description                                         |
| ---------- | ------ | --------------------------------------------------- |
| `group_id` | string | Group ID, for example `sip:fire@mcptt.example.com`. |

### Response Codes

| Code  | Description                 |
| ----- | --------------------------- |
| `404` | No group has this group ID. |

### Sample Response

```
{
    "result": {
        "group_id": "sip:police@mcptt.example.com",
        "display_name": "Police",
        "hang_timer_ms": 10000,
        "maximum_duration_ms": 600000,
        "release_on_initiator_leave": true,
        "members": [
            {
                "mcptt_id": "sip:bob@mcptt.example.com",
                "recvonly": false,
                "implicit_affiliation": false
            },
            {
                "mcptt_id": "sip:carol@mcptt.example.com",
                "recvonly": false,
                "implicit_affiliation": true
            }
        ]
    }
}
```

## Create a Group

This path creates a group.

| Method | Path                   |
| ------ | ---------------------- |
| POST   | `/api/v1/mcptt/groups` |

### Request Body

| Field                            | Type    | Required | Description                                                                                              |
| -------------------------------- | ------- | -------- | -------------------------------------------------------------------------------------------------------- |
| `group_id`                       | string  | Yes      | SIP URI. Unique among users and groups.                                                                  |
| `display_name`                   | string  | No       | Up to 64 characters.                                                                                     |
| `hang_timer_ms`                  | int     | No       |                                                                                                          |
| `maximum_duration_ms`            | int     | Yes      | At least 1.                                                                                              |
| `release_on_initiator_leave`     | boolean | No       | Default `false`.                                                                                         |
| `members`                        | array   | No       |                                                                                                          |
| `members[].mcptt_id`             | string  | Yes      | MCPTT ID of a user.                                                                                      |
| `members[].recvonly`             | boolean | No       | Default `false`.                                                                                         |
| `members[].implicit_affiliation` | boolean | No       | Adds the group to, or removes it from, the user's `implicit_affiliations`. Absent leaves them unchanged. |

### Response Codes

| Code  | Description                                                                            |
| ----- | -------------------------------------------------------------------------------------- |
| `409` | The request conflicts with other users or groups, such as a member that is not a user. |

### Sample Request

```
{
    "group_id": "sip:fire@mcptt.example.com",
    "display_name": "Fire",
    "maximum_duration_ms": 300000,
    "members": [
        {
            "mcptt_id": "sip:alice@mcptt.example.com",
            "implicit_affiliation": true
        },
        {
            "mcptt_id": "sip:bob@mcptt.example.com",
            "recvonly": true
        }
    ]
}
```

### Sample Response

```
{
    "result": {
        "group_id": "sip:fire@mcptt.example.com",
        "display_name": "Fire",
        "maximum_duration_ms": 300000,
        "release_on_initiator_leave": false,
        "members": [
            {
                "mcptt_id": "sip:alice@mcptt.example.com",
                "recvonly": false,
                "implicit_affiliation": true
            },
            {
                "mcptt_id": "sip:bob@mcptt.example.com",
                "recvonly": true,
                "implicit_affiliation": false
            }
        ]
    }
}
```

## Update a Group

This path replaces a group, members included. The request body is that of [Create a Group](#create-a-group); `group_id` is optional and, if present, must be that of the path. A member removed loses the group from its `implicit_affiliations`.

| Method | Path                              |
| ------ | --------------------------------- |
| PUT    | `/api/v1/mcptt/groups/{group_id}` |

### Path Parameters

| Name       | Type   | Description |
| ---------- | ------ | ----------- |
| `group_id` | string | Group ID.   |

### Response Codes

| Code  | Description                                                                            |
| ----- | -------------------------------------------------------------------------------------- |
| `404` | No group has this group ID.                                                            |
| `409` | The request conflicts with other users or groups, such as a member that is not a user. |

### Sample Request

```
{
    "display_name": "Police",
    "hang_timer_ms": 10000,
    "maximum_duration_ms": 600000,
    "release_on_initiator_leave": true,
    "members": [
        {
            "mcptt_id": "sip:bob@mcptt.example.com"
        },
        {
            "mcptt_id": "sip:carol@mcptt.example.com",
            "implicit_affiliation": true
        }
    ]
}
```

### Sample Response

```
{
    "result": {
        "group_id": "sip:police@mcptt.example.com",
        "display_name": "Police",
        "hang_timer_ms": 10000,
        "maximum_duration_ms": 600000,
        "release_on_initiator_leave": true,
        "members": [
            {
                "mcptt_id": "sip:bob@mcptt.example.com",
                "recvonly": false,
                "implicit_affiliation": false
            },
            {
                "mcptt_id": "sip:carol@mcptt.example.com",
                "recvonly": false,
                "implicit_affiliation": true
            }
        ]
    }
}
```

## Delete a Group

This path deletes a group. The group leaves the `implicit_affiliations` of its users, its affiliations are removed, and its call, if any, is released.

| Method | Path                              |
| ------ | --------------------------------- |
| DELETE | `/api/v1/mcptt/groups/{group_id}` |

### Path Parameters

| Name       | Type   | Description |
| ---------- | ------ | ----------- |
| `group_id` | string | Group ID.   |

### Response Codes

| Code  | Description                 |
| ----- | --------------------------- |
| `404` | No group has this group ID. |

### Sample Response

```
{
    "result": {
        "message": "group deleted"
    }
}
```

## Add or Replace a Member

This path adds a user to the members of a group, after the others, or replaces the member where it is. It returns the group.

| Method | Path                                                 |
| ------ | ---------------------------------------------------- |
| PUT    | `/api/v1/mcptt/groups/{group_id}/members/{mcptt_id}` |

### Path Parameters

| Name       | Type   | Description           |
| ---------- | ------ | --------------------- |
| `group_id` | string | Group ID.             |
| `mcptt_id` | string | MCPTT ID of the user. |

### Request Body

| Field                  | Type    | Required | Description                                                                                              |
| ---------------------- | ------- | -------- | -------------------------------------------------------------------------------------------------------- |
| `mcptt_id`             | string  | No       | Must be that of the path.                                                                                |
| `recvonly`             | boolean | No       | Default `false`.                                                                                         |
| `implicit_affiliation` | boolean | No       | Adds the group to, or removes it from, the user's `implicit_affiliations`. Absent leaves them unchanged. |

The body is required; `{}` takes the defaults.

### Response Codes

| Code  | Description                 |
| ----- | --------------------------- |
| `200` | The member was replaced.    |
| `201` | The member was added.       |
| `404` | No group has this group ID. |
| `409` | The user does not exist.    |

### Sample Request

```
{
    "recvonly": false,
    "implicit_affiliation": true
}
```

### Sample Response

```
{
    "result": {
        "group_id": "sip:fire@mcptt.example.com",
        "display_name": "Fire",
        "maximum_duration_ms": 300000,
        "release_on_initiator_leave": false,
        "members": [
            {
                "mcptt_id": "sip:alice@mcptt.example.com",
                "recvonly": false,
                "implicit_affiliation": true
            },
            {
                "mcptt_id": "sip:bob@mcptt.example.com",
                "recvonly": false,
                "implicit_affiliation": true
            }
        ]
    }
}
```

## Remove a Member

This path removes a user from the members of a group. The group leaves the user's `implicit_affiliations`, and the user's clients are de-affiliated from it and leave its call, if any. It returns the group.

| Method | Path                                                 |
| ------ | ---------------------------------------------------- |
| DELETE | `/api/v1/mcptt/groups/{group_id}/members/{mcptt_id}` |

### Path Parameters

| Name       | Type   | Description             |
| ---------- | ------ | ----------------------- |
| `group_id` | string | Group ID.               |
| `mcptt_id` | string | MCPTT ID of the member. |

### Response Codes

| Code  | Description                                              |
| ----- | -------------------------------------------------------- |
| `404` | No group has this group ID, or the user is not a member. |
| `409` | The group would be invalid without the member.           |

### Sample Response

```
{
    "result": {
        "group_id": "sip:fire@mcptt.example.com",
        "display_name": "Fire",
        "maximum_duration_ms": 300000,
        "release_on_initiator_leave": false,
        "members": [
            {
                "mcptt_id": "sip:alice@mcptt.example.com",
                "recvonly": false,
                "implicit_affiliation": true
            }
        ]
    }
}
```
