import type { APIRequestContext } from "@playwright/test";

export const ADMIN_EMAIL = "e2e-admin@ellanetworks.com";
export const ADMIN_PASSWORD = "E2eAdminPassw0rd!";

export const RoleID = {
  Admin: 1,
  ReadOnly: 2,
  NetworkManager: 3,
} as const;

type Method = "get" | "post" | "put" | "delete";

export async function send(
  request: APIRequestContext,
  method: Method,
  path: string,
  opts: { token?: string; data?: unknown } = {},
) {
  return request[method](path, {
    data: opts.data as never,
    headers: opts.token ? { Authorization: `Bearer ${opts.token}` } : undefined,
  });
}

export async function json<T>(
  request: APIRequestContext,
  method: Method,
  path: string,
  opts: { token?: string; data?: unknown } = {},
): Promise<T> {
  const response = await send(request, method, path, opts);

  if (!response.ok()) {
    throw new Error(
      `${method.toUpperCase()} ${path} -> ${response.status()} ${await response.text()}`,
    );
  }

  const body = await response.text();
  if (!body) return undefined as T;
  return (JSON.parse(body) as { result: T }).result;
}

async function isInitialized(request: APIRequestContext): Promise<boolean> {
  const status = await json<{ initialized: boolean }>(
    request,
    "get",
    "/api/v1/status",
  );
  return status.initialized;
}

export async function ensureInitialized(
  request: APIRequestContext,
): Promise<void> {
  if (await isInitialized(request)) return;

  const response = await send(request, "post", "/api/v1/init", {
    data: { email: ADMIN_EMAIL, password: ADMIN_PASSWORD },
  });

  if (!response.ok() && !(await isInitialized(request))) {
    throw new Error(
      `could not initialize the IMS: ${response.status()} ${await response.text()}`,
    );
  }
}
