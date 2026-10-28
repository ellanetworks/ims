import { RoleID } from "@/queries/users";

export interface AccessClaims {
  email: string;
  role_id: RoleID;
  exp: number;
}

// decodeAccessToken reads the claims of an access token, without checking it: the server does on each request. It
// returns null for anything that is not a JWT with them, or with a role this UI does not know.
export function decodeAccessToken(token: string): AccessClaims | null {
  const payload = token.split(".")[1];
  if (!payload) return null;

  try {
    const base64 = payload.replace(/-/g, "+").replace(/_/g, "/");
    const bytes = Uint8Array.from(atob(base64), (c) => c.charCodeAt(0));
    const claims = JSON.parse(new TextDecoder().decode(bytes));
    if (
      typeof claims.email !== "string" ||
      typeof claims.role_id !== "number" ||
      !Object.values(RoleID).includes(claims.role_id) ||
      typeof claims.exp !== "number"
    ) {
      return null;
    }
    return claims as AccessClaims;
  } catch {
    return null;
  }
}
