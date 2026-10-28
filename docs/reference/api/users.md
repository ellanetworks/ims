---
description: RESTful API reference for managing users and API tokens in Ella IMS.
---

# Users

Paths under `/api/v1/users/me` act on the user of the token. The others require Admin, and an access token: an API token gets `403`.

## Get My User

| Method | Path                |
| ------ | ------------------- |
| GET    | `/api/v1/users/me`  |

### Sample Response

```json
{
    "result": {
        "email": "admin@example.com",
        "role_id": 1
    }
}
```

## Update My Password

This path ends the user's other sessions. It requires an access token.

| Method | Path                        |
| ------ | --------------------------- |
| PUT    | `/api/v1/users/me/password` |

### Request Body

| Field              | Type   | Required | Description |
| ------------------ | ------ | -------- | ----------- |
| `current_password` | string | Yes      | Current password. |
| `password`         | string | Yes      | New password, 8 to 72 bytes. |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `400` | Wrong current password, or invalid new password. |
| `429` | More than 100 wrong passwords in an hour for the account, counting failed logins. |

## List Users

| Method | Path            |
| ------ | --------------- |
| GET    | `/api/v1/users` |

### Query Parameters

| Name       | In    | Type | Default | Allowed | Description |
| ---------- | ----- | ---- | ------- | ------- | ----------- |
| `page`     | query | int  | `1`     | `>= 1`  | 1-based page index. |
| `per_page` | query | int  | `25`    | `1…100` | Number of items per page. |

### Sample Response

```json
{
    "result": {
        "items": [
            {
                "email": "admin@example.com",
                "role_id": 1
            },
            {
                "email": "noc@example.com",
                "role_id": 2
            }
        ],
        "page": 1,
        "per_page": 25,
        "total_count": 2
    }
}
```

## Create a User

At most 50 users.

| Method | Path            |
| ------ | --------------- |
| POST   | `/api/v1/users` |

### Request Body

| Field      | Type   | Required | Description |
| ---------- | ------ | -------- | ----------- |
| `email`    | string | Yes      | Email address. Stored in lower case. |
| `password` | string | Yes      | 8 to 72 bytes. |
| `role_id`  | int    | Yes      | `1` to `3`. See [roles](index.md#roles). |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `201` | Created. |
| `400` | Invalid field, or 50 users exist. |
| `409` | A user has the email, in any case. |

## Get a User

| Method | Path                      |
| ------ | ------------------------- |
| GET    | `/api/v1/users/{email}`   |

## Update a User

This path changes the role of a user, and ends its sessions. An Admin cannot change its own role.

| Method | Path                      |
| ------ | ------------------------- |
| PUT    | `/api/v1/users/{email}`   |

### Request Body

| Field     | Type | Required | Description |
| --------- | ---- | -------- | ----------- |
| `role_id` | int  | Yes      | `1` to `3`. |

## Update a User's Password

This path ends the user's sessions. An Admin cannot update its own password here.

| Method | Path                               |
| ------ | ---------------------------------- |
| PUT    | `/api/v1/users/{email}/password`   |

### Request Body

| Field      | Type   | Required | Description |
| ---------- | ------ | -------- | ----------- |
| `password` | string | Yes      | 8 to 72 bytes. |

## Delete a User

This path deletes the user with its sessions and API tokens. An Admin cannot delete itself.

| Method | Path                      |
| ------ | ------------------------- |
| DELETE | `/api/v1/users/{email}`   |

## Create an API Token

This path returns the token, the only time it is shown. At most 12 per user. Requires an access token.

| Method | Path                                    |
| ------ | --------------------------------------- |
| POST   | `/api/v1/users/me/api-tokens`           |
| POST   | `/api/v1/users/{email}/api-tokens`      |

### Request Body

| Field        | Type   | Required | Description |
| ------------ | ------ | -------- | ----------- |
| `name`       | string | Yes      | 3 to 50 characters, unique among the user's tokens. |
| `expires_at` | string | No       | RFC 3339 time in the future. Without it, the token does not expire. |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `201` | Created. |
| `400` | Invalid field, or 12 tokens exist. |
| `409` | The user has a token of that name. |

### Sample Request

```json
{
    "name": "Monitoring",
    "expires_at": "2027-10-27T00:00:00Z"
}
```

### Sample Response

```json
{
    "result": {
        "token": "ellaims_aB3dE5fG7hJ9_KQ4ZN2X7WJ5R3TQY6PLM2VA4CF"
    }
}
```

## List API Tokens

| Method | Path                                    |
| ------ | --------------------------------------- |
| GET    | `/api/v1/users/me/api-tokens`           |
| GET    | `/api/v1/users/{email}/api-tokens`      |

### Query Parameters

| Name       | In    | Type | Default | Allowed | Description |
| ---------- | ----- | ---- | ------- | ------- | ----------- |
| `page`     | query | int  | `1`     | `>= 1`  | 1-based page index. |
| `per_page` | query | int  | `25`    | `1…100` | Number of items per page. |

### Response Fields

| Field          | Type   | Description |
| -------------- | ------ | ----------- |
| `id`           | string | Token identifier: the part of the token between `ellaims_` and the next `_`. |
| `name`         | string | Name. |
| `created_at`   | string | Creation time. |
| `expires_at`   | string | Expiry time. Absent for a token that does not expire. |
| `last_used_at` | string | Time of the last use, to the minute. Absent for a token never used. |

### Sample Response

```json
{
    "result": {
        "items": [
            {
                "id": "aB3dE5fG7hJ9",
                "name": "Monitoring",
                "created_at": "2026-10-27T18:44:19.436Z",
                "expires_at": "2027-10-27T00:00:00.000Z",
                "last_used_at": "2026-10-27T19:02:00.000Z"
            }
        ],
        "page": 1,
        "per_page": 25,
        "total_count": 1
    }
}
```

## Delete an API Token

| Method | Path                                         |
| ------ | -------------------------------------------- |
| DELETE | `/api/v1/users/me/api-tokens/{id}`           |
| DELETE | `/api/v1/users/{email}/api-tokens/{id}`      |
