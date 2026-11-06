# Manage Single Sign-On Users

These guides manage users who sign in through the identity provider. [Configure single sign-on](https://docs.ellanetworks.com/ims/how_to/configure_single_sign_on/index.md) first.

## Give a user access

1. In the UI, go to **Users** and click **Create**. Set:
   - **Email**: the user's email address at the identity provider
   - **Password**: off
   - **Role**: the user's role
   - **Pin role**: checked to keep this role, if groups are mapped to roles
1. Click **Create**.

The user signs in with **Log in with** the display name. Their first login links their user to their account at the identity provider.

## Map groups to roles

Users in a mapped group get their role from it at each login, and get a user at their first login. Users in no mapped group cannot sign in through the identity provider, unless their role is [pinned](#pin-a-users-role).

### 1. Send groups to Ella IMS

1. Go to **Clients**, open the `ims` client, and open the **Client scopes** tab.
1. Click `ims-dedicated`, then **Configure a new mapper**, or **Add mapper** > **By configuration**, then **Group Membership**.
1. Set **Name** and **Token Claim Name** to `groups`. Turn off **Full group path**. Keep **Add to ID token** on.
1. Click **Save**.

The groups are group names, such as `ims-admins`.

Configure the provider to send the user's groups in a claim, such as `groups`, as a list of group names or IDs.

### 2. Map the groups in Ella IMS

1. Go to **Single Sign-On** and, in **Roles from groups**, click **Edit**.
1. Set **Groups claim** to `groups`.
1. Click **Add group** for each group, and set the group and its role. Put the most privileged first: a user in several groups gets the role of the first.
1. Click **Update**.

## Pin a user's role

A pinned role is kept whatever the user's groups.

1. Go to **Users** and open the user.
1. In **Account**, edit the role: choose the role, check **Pin role**, and click **Update**. Changing a role that comes from groups checks it for you.

To unpin, uncheck **Pin role**. The role then follows the user's groups from their next login.

## Unlink a user

Unlink a user whose account at the identity provider was replaced, so that their next login links the new account by email.

1. Go to **Users** and open the user.
1. In **Account**, click **Unlink**, then **Unlink**.

## Offboard a user

1. Remove or disable the user at the identity provider. They can no longer sign in, but their current session lasts until the **Session lifetime** ends.
1. To end their access at once, go to **Users** in Ella IMS, open the user, and click **Delete**. This also deletes their API tokens.

To keep the user instead, open the user and:

1. In **Account**, click **Unlink**, then **Unlink**. This ends their single sign-on sessions.
1. Click **Remove Password**, then **Remove**, if they have one.
1. In **API Tokens**, delete each token.
