import { RoleID } from "@/queries/users";

const base64url = (value: unknown) =>
  btoa(JSON.stringify(value))
    .replace(/=+$/, "")
    .replace(/\+/g, "-")
    .replace(/\//g, "_");

// makeToken is an access token with the claims the UI reads, and an invalid signature, which only the server checks.
export const makeToken = (
  email = "admin@example.com",
  roleId = RoleID.Admin,
  expiresInSeconds = 900,
) =>
  [
    base64url({ alg: "HS256", typ: "JWT" }),
    base64url({
      id: 1,
      email,
      role_id: roleId,
      sid: 1,
      exp: Math.floor(Date.now() / 1000) + expiresInSeconds,
    }),
    "signature",
  ].join(".");
