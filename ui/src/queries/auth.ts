import { apiFetch } from "@/queries/utils";

export interface TokenResponse {
  token: string;
}

export const login = (email: string, password: string) =>
  apiFetch<TokenResponse>("/api/v1/auth/login", {
    method: "POST",
    body: { email, password },
    anonymous: true,
  });

export const initialize = (email: string, password: string) =>
  apiFetch<TokenResponse>("/api/v1/init", {
    method: "POST",
    body: { email, password },
    anonymous: true,
  });

export const refresh = () =>
  apiFetch<TokenResponse>("/api/v1/auth/refresh", {
    method: "POST",
    anonymous: true,
  });

export interface LogoutResponse {
  redirect_url: string;
}

export const logout = async (): Promise<string | undefined> => {
  const resp = await apiFetch<LogoutResponse | undefined>(
    "/api/v1/auth/logout",
    { method: "POST", anonymous: true },
  );
  return resp?.redirect_url || undefined;
};
