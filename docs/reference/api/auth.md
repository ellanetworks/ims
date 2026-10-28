---
description: RESTful API reference for logging in to Ella IMS.
---

# Authentication

## Log In

This path returns an access token, and sets the session cookie, `ims_session`, or `__Host-ims_session` over TLS. The session lasts 30 days, and a user has at most 10.

| Method | Path                 |
| ------ | -------------------- |
| POST   | `/api/v1/auth/login` |

### Request Body

| Field      | Type   | Required | Description |
| ---------- | ------ | -------- | ----------- |
| `email`    | string | Yes      | Email address, in any case. |
| `password` | string | Yes      | Password. |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `401` | Wrong email or password. |
| `429` | More than 20 attempts in a minute from the client address, or its IPv6 /64; or more than 100 wrong passwords in an hour for the account. |

### Sample Response

```json
{
    "result": {
        "message": "Login successful",
        "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."
    }
}
```

## Refresh the Access Token

This path returns a new access token for the session of the cookie.

| Method | Path                   |
| ------ | ---------------------- |
| POST   | `/api/v1/auth/refresh` |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `401` | No session, or the session ended. The response clears the cookie. |

### Sample Response

```json
{
    "result": {
        "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."
    }
}
```

## Log Out

This path ends the session of the cookie, and its access tokens, and clears the cookie.

| Method | Path                  |
| ------ | --------------------- |
| POST   | `/api/v1/auth/logout` |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `204` | Logged out, or no session. |
