# Configure Single Sign-On

This guide lets users sign in to the Ella IMS UI through an OpenID Connect identity provider. It gives the steps for Keycloak; other providers are set up the same way.

## Prerequisites

- **Ella IMS**: served over HTTPS, at a URL with no path.
- **Identity provider**: an OpenID Connect provider, reachable from Ella IMS over HTTPS.
- **Ella IMS account**: an Admin that signs in with a password.

The examples use these values:

| Value                                               | Role                                                                    |
| --------------------------------------------------- | ----------------------------------------------------------------------- |
| `https://ims.example.com`                           | Ella IMS's external URL                                                 |
| `https://ims.example.com/api/v1/auth/oidc/callback` | Redirect URL: the external URL followed by `/api/v1/auth/oidc/callback` |

## 1. Register Ella IMS at the identity provider

In the realm users sign in to:

1. Go to **Clients** and click **Create client**.
1. Set **Client type** to **OpenID Connect** and **Client ID** to `ims`. Click **Next**.
1. Turn on **Client authentication**. Keep **Standard flow** checked. Click **Next**.
1. Set **Valid redirect URIs** to the redirect URL. Click **Save**.
1. In the client's **Credentials** tab, copy the **Client Secret**.

In **Users**, turn on **Email verified** for each user.

1. Register a web application, or confidential client, with the redirect URL.
1. Create a client secret for it.
1. Allow the `openid`, `email` and `profile` scopes.

If the provider does not send `email_verified`, turn on **Trust email addresses from this provider** in step 2.

## 2. Configure Ella IMS

In the UI, go to **Single Sign-On** and, in **Provider**, click **Edit**. Set:

| Field                                        | Value                                                                                       |
| -------------------------------------------- | ------------------------------------------------------------------------------------------- |
| **External URL**                             | `https://ims.example.com`                                                                   |
| **Issuer URL**                               | Keycloak: `https://<keycloak-host>/realms/<realm>`. Otherwise, from the provider.           |
| **CA certificate**                           | The PEM CA certificate of the identity provider, only if a private CA signs its certificate |
| **Client ID**                                | Keycloak: `ims`. Otherwise, from the provider.                                              |
| **Client secret**                            | The secret from step 1                                                                      |
| **Display name**                             | The name on the login button, such as `Keycloak`                                            |
| **Scopes**                                   | `openid email profile`                                                                      |
| **Session lifetime**                         | `24` hours, or less to end access sooner after offboarding                                  |
| **Trust email addresses from this provider** | Off, unless the provider does not send `email_verified`                                     |

Check that the **Redirect URL** under the external URL is the one registered in step 1.

## 3. Test the identity provider

In the dialog, click **Test**. Fix the issuer URL or the CA certificate until it shows **Works**. Click **Update**.

## 4. Enable single sign-on

On the **Single Sign-On** page, click **Enable**.

To turn it off, for example during an incident, click **Disable**. Everyone who logged in with single sign-on is signed out.

## 5. Sign in

1. Give a user access: [create their user](https://docs.ellanetworks.com/ims/how_to/manage_single_sign_on_users/#give-a-user-access), or [map their groups to a role](https://docs.ellanetworks.com/ims/how_to/manage_single_sign_on_users/#map-groups-to-roles).
1. In a private browser window, open `https://ims.example.com`, and click **Log in with** your display name.
1. Sign in at the identity provider. Ella IMS opens.

If the login page shows an error, the `auth_oidc_login` [audit event](https://docs.ellanetworks.com/ims/reference/observability/#audit-events) in the logs gives the reason.
