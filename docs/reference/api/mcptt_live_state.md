---
description: RESTful API reference for viewing MCPTT registrations, affiliations and group calls.
---

# MCPTT Live State

The registrations, affiliations and group calls the MCPTT AS holds in memory. The `/api/v1/mcptt` paths answer `404` when the IMS does not run the MCPTT AS.

## List Registrations

This path returns the list of MCPTT clients bound to their users by service authorisation, ordered by MCPTT ID and client ID.

| Method | Path                          |
| ------ | ----------------------------- |
| GET    | `/api/v1/mcptt/registrations` |

### Query Parameters

| Name       | In    | Type | Default | Allowed | Description |
| ---------- | ----- | ---- | ------- | ------- | ----------- |
| `mcptt_id` | query | str  |         |         | Filter by MCPTT ID. |
| `page`     | query | int  | `1`     | `>= 1`  | 1-based page index. |
| `per_page` | query | int  | `25`    | `1…100` | Number of items per page. |

### Response Fields

| Field        | Type   | Description |
| ------------ | ------ | ----------- |
| `mcptt_id`   | string | MCPTT ID of the user. |
| `client_id`  | string | MCPTT client ID (TS 24.379 §4.10). |
| `impu`       | string | IMS public user identity the client registered at. |
| `impus`      | array  | Public user identities the client is served at, `impu` first. |
| `since`      | string | When the client was authorised for the user (RFC 3339). Kept across refreshes. |
| `expires_at` | string | Expiry time (RFC 3339). |
| `contacts`   | array  | Contacts the client registered, in direct mode. Empty through the IMS. |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `503` | The MCPTT AS is restarting or busy. |

### Sample Response

```json
{
    "result": {
        "items": [
            {
                "mcptt_id": "sip:alice@mcptt.example.com",
                "client_id": "urn:uuid:00000000-0000-1000-8000-000000000001",
                "impu": "sip:15550000001@ims.mnc001.mcc001.3gppnetwork.org",
                "impus": [
                    "sip:15550000001@ims.mnc001.mcc001.3gppnetwork.org",
                    "tel:+15550000001"
                ],
                "since": "2026-10-26T18:40:02.118Z",
                "expires_at": "2026-10-26T19:40:02.118Z",
                "contacts": []
            }
        ],
        "page": 1,
        "per_page": 25,
        "total_count": 1
    }
}
```

## List Affiliations

This path returns the list of affiliations of clients to groups, ordered by group ID, MCPTT ID and client ID.

| Method | Path                         |
| ------ | ---------------------------- |
| GET    | `/api/v1/mcptt/affiliations` |

### Query Parameters

| Name       | In    | Type | Default | Allowed | Description |
| ---------- | ----- | ---- | ------- | ------- | ----------- |
| `mcptt_id` | query | str  |         |         | Filter by MCPTT ID. |
| `group_id` | query | str  |         |         | Filter by group ID. |
| `page`     | query | int  | `1`     | `>= 1`  | 1-based page index. |
| `per_page` | query | int  | `25`    | `1…100` | Number of items per page. |

### Response Fields

| Field       | Type    | Description |
| ----------- | ------- | ----------- |
| `mcptt_id`  | string  | MCPTT ID of the user. |
| `client_id` | string  | MCPTT client ID. |
| `group_id`  | string  | Group ID. |
| `status`    | string  | Affiliation status at the participating function (TS 24.379 §9.2.2.2.2): `affiliating`, `affiliated` or `deaffiliating`. The first two count as affiliated. |
| `implicit`  | boolean | `true` if the server affiliated the client from the user's `implicit_affiliations` (TS 24.379 §9.2.2.2.15); `false` if the client affiliated itself (§9.2.2.2.3). |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `503` | The MCPTT AS is restarting or busy. |

### Sample Response

```json
{
    "result": {
        "items": [
            {
                "mcptt_id": "sip:alice@mcptt.example.com",
                "client_id": "urn:uuid:00000000-0000-1000-8000-000000000001",
                "group_id": "sip:fire@mcptt.example.com",
                "status": "affiliated",
                "implicit": true
            }
        ],
        "page": 1,
        "per_page": 25,
        "total_count": 1
    }
}
```

## List Group Calls

This path returns the list of group calls, oldest first.

| Method | Path                  |
| ------ | --------------------- |
| GET    | `/api/v1/mcptt/calls` |

### Query Parameters

| Name       | In    | Type | Default | Allowed | Description |
| ---------- | ----- | ---- | ------- | ------- | ----------- |
| `mcptt_id` | query | str  |         |         | Filter by participant MCPTT ID. |
| `group_id` | query | str  |         |         | Filter by group ID. |
| `page`     | query | int  | `1`     | `>= 1`  | 1-based page index. |
| `per_page` | query | int  | `25`    | `1…100` | Number of items per page. |

### Response Fields

| Field                     | Type    | Description |
| ------------------------- | ------- | ----------- |
| `id`                      | string  | Call identifier, the "gr" of the MCPTT session identity. |
| `session_id`              | string  | MCPTT session identity (TS 24.379 §4.5). |
| `group_id`                | string  | Group ID. |
| `originator`              | string  | MCPTT ID of the member that started the call. |
| `created_at`              | string  | Creation time (RFC 3339). |
| `started_at`              | string  | When the originator was answered (RFC 3339). `null` while the call is set up. |
| `ends_at`                 | string  | When the maximum duration (TNG3) releases the call (RFC 3339). `null` if none. |
| `talker`                  | object  | Participant with the floor, with its `mcptt_id` and `since`. `null` if the floor is idle. |
| `participants`            | array   | Members invited and joined, the originator first. |
| `participants[].mcptt_id` | string  | MCPTT ID of the participant. |
| `participants[].state`    | string  | `invited` or `joined`. |
| `participants[].required` | boolean | Whether the participant is required. |
| `participants[].recvonly` | boolean | Whether the participant may listen but not talk. |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `503` | The MCPTT AS is restarting or busy. |

### Sample Response

```json
{
    "result": {
        "items": [
            {
                "id": "XBDRKQ5VJ7T4M2N6P3HYWLC7AE",
                "session_id": "sip:mcptt-controlling@mcptt.ims.mnc001.mcc001.3gppnetwork.org;gr=XBDRKQ5VJ7T4M2N6P3HYWLC7AE",
                "group_id": "sip:fire@mcptt.example.com",
                "originator": "sip:alice@mcptt.example.com",
                "created_at": "2026-10-26T18:45:10.402Z",
                "started_at": "2026-10-26T18:45:10.655Z",
                "ends_at": "2026-10-26T18:50:10.655Z",
                "talker": {
                    "mcptt_id": "sip:alice@mcptt.example.com",
                    "since": "2026-10-26T18:45:11.020Z"
                },
                "participants": [
                    {
                        "mcptt_id": "sip:alice@mcptt.example.com",
                        "state": "joined",
                        "required": false,
                        "recvonly": false
                    },
                    {
                        "mcptt_id": "sip:bob@mcptt.example.com",
                        "state": "invited",
                        "required": false,
                        "recvonly": true
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

## Get a Group Call

This path returns a specific group call.

| Method | Path                            |
| ------ | ------------------------------- |
| GET    | `/api/v1/mcptt/calls/{call_id}` |

### Path Parameters

| Name      | Type   | Description |
| --------- | ------ | ----------- |
| `call_id` | string | Call identifier. |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `404` | No call has this identifier. |
| `503` | The MCPTT AS is restarting or busy. |

### Sample Response

```json
{
    "result": {
        "id": "XBDRKQ5VJ7T4M2N6P3HYWLC7AE",
        "session_id": "sip:mcptt-controlling@mcptt.ims.mnc001.mcc001.3gppnetwork.org;gr=XBDRKQ5VJ7T4M2N6P3HYWLC7AE",
        "group_id": "sip:fire@mcptt.example.com",
        "originator": "sip:alice@mcptt.example.com",
        "created_at": "2026-10-26T18:45:10.402Z",
        "started_at": "2026-10-26T18:45:10.655Z",
        "ends_at": "2026-10-26T18:50:10.655Z",
        "talker": null,
        "participants": [
            {
                "mcptt_id": "sip:alice@mcptt.example.com",
                "state": "joined",
                "required": false,
                "recvonly": false
            },
            {
                "mcptt_id": "sip:bob@mcptt.example.com",
                "state": "joined",
                "required": false,
                "recvonly": true
            }
        ]
    }
}
```
