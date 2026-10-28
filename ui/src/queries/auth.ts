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

export const logout = () =>
  apiFetch<void>("/api/v1/auth/logout", { method: "POST", anonymous: true });
