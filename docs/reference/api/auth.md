---
description: RESTful API reference for logging in to Ella IMS.
---

# Authentication

## Log In

This path returns an access token, and sets the session cookie, `ims_session`, or `__Host-ims_session` over TLS, valid for 30 days. A user has at most 10 sessions.

| Method | Path                 |
| ------ | -------------------- |
| POST   | `/api/v1/auth/login` |

### Request Body

| Field      | Type   | Required | Description |
| ---------- | ------ | -------- | ----------- |
| `email`    | string | Yes      | Email address, in any case. |
| `password` | string | Yes      | Password. |

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

### Sample Response

```json
{
    "result": {
        "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."
    }
}
```

## Log Out

This path ends the session of the cookie, and its access tokens, and clears the cookie. For a single sign-on session, it returns the identity provider's logout URL.

| Method | Path                  |
| ------ | --------------------- |
| POST   | `/api/v1/auth/logout` |

### Sample Response

```json
{
    "result": {
        "redirect_url": "https://idp.example.com/realms/ims/protocol/openid-connect/logout?client_id=ims&post_logout_redirect_uri=https%3A%2F%2Fims.example.com%2Flogin"
    }
}
```

## Get the Single Sign-On Option

This path returns whether single sign-on is enabled.

| Method | Path                |
| ------ | ------------------- |
| GET    | `/api/v1/auth/oidc` |

### Sample Response

```json
{
    "result": {
        "enabled": true,
        "display_name": "Keycloak"
    }
}
```

## Log In Through the Identity Provider

This path redirects the browser to the identity provider.

| Method | Path                      |
| ------ | ------------------------- |
| GET    | `/api/v1/auth/oidc/login` |

### Query Parameters

| Name   | In    | Type   | Description |
| ------ | ----- | ------ | ----------- |
| `link` | query | string | `1` links the identity provider account to the user of the session cookie, and redirects to `/profile`, or `/profile?error=<error>`. |

## Return From the Identity Provider

This path is the redirect URL. On success, it sets the session cookie and redirects to `/`. Otherwise, it redirects to `/login?error=<error>`.

| Method | Path                         |
| ------ | ---------------------------- |
| GET    | `/api/v1/auth/oidc/callback` |

### Errors

| Error                | Description |
| -------------------- | ----------- |
| `rate_limited`       | Too many logins from the address. |
| `unavailable`        | Single sign-on is off, or the identity provider cannot be reached. |
| `failed`             | The login expired, was already used or is invalid, the single sign-on settings changed during it, or the identity provider returned an error. |
| `denied`             | The user or the identity provider refused access. |
| `not_set_up`         | The identity provider gives no email, or no user has it and no group mapping is set. |
| `email_not_verified` | The identity provider marks the email as not verified, or does not say and [`trust_unverified_email`](oidc.md#update-the-oidc-settings) is off. |
| `linked_elsewhere`   | The user with the email is linked to another account at the identity provider. |
| `link_required`      | The user has a password and must link the account from their profile. |
| `no_group`           | None of the user's groups is mapped to a role. |
| `groups_overage`     | The user is in too many groups for the identity provider to list them. |
| `too_many_users`     | 50 users exist. |
| `error`              | Internal error. |
