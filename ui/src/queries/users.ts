import {
  apiFetch,
  withQuery,
  type Page,
  type PageParams,
} from "@/queries/utils";

export enum RoleID {
  Admin = 1,
  ReadOnly = 2,
  NetworkManager = 3,
}

export const roleLabels: Record<RoleID, string> = {
  [RoleID.Admin]: "Admin",
  [RoleID.ReadOnly]: "Read Only",
  [RoleID.NetworkManager]: "Network Manager",
};

export const roleDescriptions: Record<RoleID, string> = {
  [RoleID.Admin]: "Everything, including users.",
  [RoleID.ReadOnly]: "Reads everything but users, and changes nothing.",
  [RoleID.NetworkManager]: "Changes settings, but not users.",
};

export interface User {
  email: string;
  role_id: RoleID;
}

export const MIN_PASSWORD_LENGTH = 8;
export const MAX_PASSWORD_LENGTH = 72;

// passwordError is why a new password is not valid, the server's rule: 8 to 72 bytes.
export const passwordError = (password: string): string | undefined => {
  const bytes = new TextEncoder().encode(password).length;
  if (bytes < MIN_PASSWORD_LENGTH) {
    return `At least ${MIN_PASSWORD_LENGTH} characters.`;
  }
  if (bytes > MAX_PASSWORD_LENGTH) {
    return `At most ${MAX_PASSWORD_LENGTH} bytes.`;
  }
  return undefined;
};

const userPath = (email: string) =>
  `/api/v1/users/${encodeURIComponent(email)}`;

export const getMyUser = () => apiFetch<User>("/api/v1/users/me");

export const updateMyPassword = (currentPassword: string, password: string) =>
  apiFetch<void>("/api/v1/users/me/password", {
    method: "PUT",
    body: { current_password: currentPassword, password },
  });

export const listUsers = ({ page = 1, perPage = 25 }: PageParams = {}) =>
  apiFetch<Page<User>>(withQuery("/api/v1/users", { page, per_page: perPage }));

export const getUser = (email: string) => apiFetch<User>(userPath(email));

export const createUser = (email: string, password: string, roleId: RoleID) =>
  apiFetch<void>("/api/v1/users", {
    method: "POST",
    body: { email, password, role_id: roleId },
  });

export const updateUserRole = (email: string, roleId: RoleID) =>
  apiFetch<void>(userPath(email), {
    method: "PUT",
    body: { role_id: roleId },
  });

export const updateUserPassword = (email: string, password: string) =>
  apiFetch<void>(`${userPath(email)}/password`, {
    method: "PUT",
    body: { password },
  });

export const deleteUser = (email: string) =>
  apiFetch<void>(userPath(email), { method: "DELETE" });
