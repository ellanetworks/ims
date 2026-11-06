# Users

Paths under `/api/v1/users/me` act on the user of the token. The others require Admin, and an access token: an API token gets `403`.

## Get My User

| Method | Path               |
| ------ | ------------------ |
| GET    | `/api/v1/users/me` |

### Sample Response

```
{
    "result": {
        "email": "admin@example.com",
        "role_id": 1,
        "role_pinned": false,
        "password_set": true,
        "oidc_linked": false
    }
}
```

### Response Fields

| Field          | Type    | Description                                                                                                                         |
| -------------- | ------- | ----------------------------------------------------------------------------------------------------------------------------------- |
| `email`        | string  | Email address.                                                                                                                      |
| `role_id`      | int     | `1` to `3`. See [roles](https://docs.ellanetworks.com/ims/reference/api/#roles).                                                    |
| `role_pinned`  | boolean | The role is kept whatever the user's [groups](https://docs.ellanetworks.com/ims/reference/api/oidc/#group-roles).                   |
| `password_set` | boolean | The user has a password.                                                                                                            |
| `oidc_linked`  | boolean | The user is linked to an account at the current [identity provider](https://docs.ellanetworks.com/ims/reference/api/oidc/index.md). |

The other paths that return users return the same fields.

## Update My Password

This path ends the user's other sessions. It requires an access token.

| Method | Path                        |
| ------ | --------------------------- |
| PUT    | `/api/v1/users/me/password` |

### Request Body

| Field              | Type   | Required | Description                  |
| ------------------ | ------ | -------- | ---------------------------- |
| `current_password` | string | Yes      | Current password.            |
| `password`         | string | Yes      | New password, 8 to 72 bytes. |

### Response Codes

| Code  | Description                                                                       |
| ----- | --------------------------------------------------------------------------------- |
| `400` | Wrong current password, or invalid new password.                                  |
| `409` | The user has no password.                                                         |
| `429` | More than 100 wrong passwords in an hour for the account, counting failed logins. |

## List Users

| Method | Path            |
| ------ | --------------- |
| GET    | `/api/v1/users` |

### Query Parameters

| Name       | In    | Type | Default | Allowed | Description               |
| ---------- | ----- | ---- | ------- | ------- | ------------------------- |
| `page`     | query | int  | `1`     | `>= 1`  | 1-based page index.       |
| `per_page` | query | int  | `25`    | `1…100` | Number of items per page. |

### Sample Response

```
{
    "result": {
        "items": [
            {
                "email": "admin@example.com",
                "role_id": 1,
                "role_pinned": false,
                "password_set": true,
                "oidc_linked": false
            },
            {
                "email": "noc@example.com",
                "role_id": 2,
                "role_pinned": false,
                "password_set": true,
                "oidc_linked": false
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

| Field         | Type    | Required | Description                                                                                                                                       |
| ------------- | ------- | -------- | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| `email`       | string  | Yes      | Email address. ASCII letters are stored in lower case.                                                                                            |
| `password`    | string  | No       | 8 to 72 bytes. Without it, the user signs in only through the [identity provider](https://docs.ellanetworks.com/ims/reference/api/oidc/index.md). |
| `role_id`     | int     | Yes      | `1` to `3`. See [roles](https://docs.ellanetworks.com/ims/reference/api/#roles).                                                                  |
| `role_pinned` | boolean | No       | Keep the role whatever the user's [groups](https://docs.ellanetworks.com/ims/reference/api/oidc/#group-roles). Default `false`.                   |

### Response Codes

| Code  | Description                        |
| ----- | ---------------------------------- |
| `201` | Created.                           |
| `400` | Invalid field, or 50 users exist.  |
| `409` | A user has the email, in any case. |

## Get a User

| Method | Path                    |
| ------ | ----------------------- |
| GET    | `/api/v1/users/{email}` |

## Update a User

This path sets the role of a user. A role change ends the user's sessions. An Admin cannot change its own role, and the last Admin with a password stays an Admin.

| Method | Path                    |
| ------ | ----------------------- |
| PUT    | `/api/v1/users/{email}` |

### Request Body

| Field         | Type    | Required | Description                                                                                                                                           |
| ------------- | ------- | -------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- |
| `role_id`     | int     | Yes      | `1` to `3`.                                                                                                                                           |
| `role_pinned` | boolean | No       | Keep the role whatever the user's [groups](https://docs.ellanetworks.com/ims/reference/api/oidc/#group-roles). Without it, the current value is kept. |

### Response Codes

| Code  | Description                                                                                                                                                                                |
| ----- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `409` | The role comes from the user's [groups](https://docs.ellanetworks.com/ims/reference/api/oidc/#group-roles) and `role_pinned` is not `true`, or the user is the last Admin with a password. |

## Update a User's Password

This path ends the user's sessions. An Admin cannot update its own password here.

| Method | Path                             |
| ------ | -------------------------------- |
| PUT    | `/api/v1/users/{email}/password` |

### Request Body

| Field      | Type   | Required | Description    |
| ---------- | ------ | -------- | -------------- |
| `password` | string | Yes      | 8 to 72 bytes. |

## Remove a User's Password

This path removes the password of a user, who can then sign in only through the [identity provider](https://docs.ellanetworks.com/ims/reference/api/oidc/index.md). It ends the user's sessions. An Admin cannot remove its own password, and the last Admin with a password keeps it.

| Method | Path                             |
| ------ | -------------------------------- |
| DELETE | `/api/v1/users/{email}/password` |

### Response Codes

| Code  | Description                                                     |
| ----- | --------------------------------------------------------------- |
| `409` | The user has no password, or is the last Admin with a password. |

## Unlink a User

This path unlinks a user from their account at the identity provider, and ends the sessions started through it. Their next login through the identity provider links them again by email. An Admin cannot unlink itself.

| Method | Path                         |
| ------ | ---------------------------- |
| DELETE | `/api/v1/users/{email}/oidc` |

### Response Codes

| Code  | Description                                                                        |
| ----- | ---------------------------------------------------------------------------------- |
| `404` | No user has the email, or the user is not linked to the current identity provider. |

## Delete a User

This path deletes the user with its sessions and API tokens. An Admin cannot delete itself, and the last Admin with a password cannot be deleted.

| Method | Path                    |
| ------ | ----------------------- |
| DELETE | `/api/v1/users/{email}` |

### Response Codes

| Code  | Description                                 |
| ----- | ------------------------------------------- |
| `409` | The user is the last Admin with a password. |

## Create an API Token

This path returns the token, the only time it is shown. At most 12 per user. Requires an access token.

| Method | Path                               |
| ------ | ---------------------------------- |
| POST   | `/api/v1/users/me/api-tokens`      |
| POST   | `/api/v1/users/{email}/api-tokens` |

### Request Body

| Field        | Type   | Required | Description                                                         |
| ------------ | ------ | -------- | ------------------------------------------------------------------- |
| `name`       | string | Yes      | 3 to 50 characters, unique among the user's tokens.                 |
| `expires_at` | string | No       | RFC 3339 time in the future. Without it, the token does not expire. |

### Response Codes

| Code  | Description                        |
| ----- | ---------------------------------- |
| `201` | Created.                           |
| `400` | Invalid field, or 12 tokens exist. |
| `409` | The user has a token of that name. |

### Sample Request

```
{
    "name": "Monitoring",
    "expires_at": "2027-10-27T00:00:00Z"
}
```

### Sample Response

```
{
    "result": {
        "token": "ellaims_aB3dE5fG7hJ9_KQ4ZN2X7WJ5R3TQY6PLM2VA4CF"
    }
}
```

## List API Tokens

| Method | Path                               |
| ------ | ---------------------------------- |
| GET    | `/api/v1/users/me/api-tokens`      |
| GET    | `/api/v1/users/{email}/api-tokens` |

### Query Parameters

| Name       | In    | Type | Default | Allowed | Description               |
| ---------- | ----- | ---- | ------- | ------- | ------------------------- |
| `page`     | query | int  | `1`     | `>= 1`  | 1-based page index.       |
| `per_page` | query | int  | `25`    | `1…100` | Number of items per page. |

### Response Fields

| Field          | Type   | Description                                                                  |
| -------------- | ------ | ---------------------------------------------------------------------------- |
| `id`           | string | Token identifier: the part of the token between `ellaims_` and the next `_`. |
| `name`         | string | Name.                                                                        |
| `created_at`   | string | Creation time.                                                               |
| `expires_at`   | string | Expiry time. Absent for a token that does not expire.                        |
| `last_used_at` | string | Time of the last use, to the minute. Absent for a token never used.          |

### Sample Response

```
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

| Method | Path                                    |
| ------ | --------------------------------------- |
| DELETE | `/api/v1/users/me/api-tokens/{id}`      |
| DELETE | `/api/v1/users/{email}/api-tokens/{id}` |
