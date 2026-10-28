import { apiFetch, withQuery, type Page } from "@/queries/utils";

export interface APIToken {
  id: string;
  name: string;
  created_at: string;
  expires_at?: string;
  last_used_at?: string;
}

// The API tokens of the logged-in user when email is undefined, else of that user.
const tokensPath = (email?: string) =>
  email === undefined
    ? "/api/v1/users/me/api-tokens"
    : `/api/v1/users/${encodeURIComponent(email)}/api-tokens`;

export const MAX_API_TOKENS = 12;

export const listAPITokens = (email?: string) =>
  apiFetch<Page<APIToken>>(
    withQuery(tokensPath(email), { page: 1, per_page: MAX_API_TOKENS }),
  );

export const createAPIToken = (
  name: string,
  expiresAt: string | undefined,
  email?: string,
) =>
  apiFetch<{ token: string }>(tokensPath(email), {
    method: "POST",
    body: expiresAt ? { name, expires_at: expiresAt } : { name },
  });

export const deleteAPIToken = (id: string, email?: string) =>
  apiFetch<void>(`${tokensPath(email)}/${encodeURIComponent(id)}`, {
    method: "DELETE",
  });
