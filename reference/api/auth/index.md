# Authentication

## Log In

This path returns an access token, and sets the session cookie, `ims_session`, or `__Host-ims_session` over TLS. The session lasts 30 days, and a user has at most 10.

| Method | Path                 |
| ------ | -------------------- |
| POST   | `/api/v1/auth/login` |

### Request Body

| Field      | Type   | Required | Description                 |
| ---------- | ------ | -------- | --------------------------- |
| `email`    | string | Yes      | Email address, in any case. |
| `password` | string | Yes      | Password.                   |

### Response Codes

| Code  | Description                                                                                                                              |
| ----- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| `401` | Wrong email or password.                                                                                                                 |
| `429` | More than 20 attempts in a minute from the client address, or its IPv6 /64; or more than 100 wrong passwords in an hour for the account. |

### Sample Response

```
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

| Code  | Description                                                       |
| ----- | ----------------------------------------------------------------- |
| `401` | No session, or the session ended. The response clears the cookie. |

### Sample Response

```
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

| Code  | Description                |
| ----- | -------------------------- |
| `204` | Logged out, or no session. |

## Get the Single Sign-On Option

This path tells the login page whether to show the button to sign in through the identity provider.

| Method | Path                |
| ------ | ------------------- |
| GET    | `/api/v1/auth/oidc` |

### Sample Response

```
{
    "result": {
        "enabled": true,
        "display_name": "Keycloak"
    }
}
```

## Sign In Through the Identity Provider

The login page sends the browser to this path, which redirects it to the identity provider. It counts toward the login limit of 20 per minute per address.

| Method | Path                      |
| ------ | ------------------------- |
| GET    | `/api/v1/auth/oidc/login` |

## Return From the Identity Provider

The identity provider sends the browser back to this path, the redirect URL. Each login completes once. On success, it sets the session cookie, for the [session lifetime](https://docs.ellanetworks.com/ims/reference/api/oidc/#get-the-oidc-settings), and redirects to `/`. Otherwise, it redirects to `/login?error=<error>`, and the `auth_oidc_login` [audit event](https://docs.ellanetworks.com/ims/reference/observability/#audit-events) gives the details.

| Method | Path                         |
| ------ | ---------------------------- |
| GET    | `/api/v1/auth/oidc/callback` |

### Errors

| Error                | Description                                                                                                                                                                                   |
| -------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `rate_limited`       | Too many logins from the address.                                                                                                                                                             |
| `unavailable`        | Single sign-on is off, or the identity provider cannot be reached.                                                                                                                            |
| `failed`             | The login expired, was already used or is invalid, the single sign-on settings changed during it, or the identity provider returned an error.                                                 |
| `denied`             | The user or the identity provider refused access.                                                                                                                                             |
| `not_set_up`         | The identity provider gives no email, or no user has it and no group mapping is set.                                                                                                          |
| `email_not_verified` | The identity provider marks the email as not verified, or does not say and [`trust_unverified_email`](https://docs.ellanetworks.com/ims/reference/api/oidc/#update-the-oidc-settings) is off. |
| `linked_elsewhere`   | The user with the email is linked to another account at the identity provider.                                                                                                                |
| `no_group`           | None of the user's groups is mapped to a role.                                                                                                                                                |
| `groups_overage`     | The user is in too many groups for the identity provider to list them.                                                                                                                        |
| `too_many_users`     | 50 users exist.                                                                                                                                                                               |
| `error`              | Internal error.                                                                                                                                                                               |
